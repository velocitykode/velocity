// Package orm - reflection.go
//
// Canonical reflection resolver for ORM model types. Every code path that
// needs to translate between Go struct fields and database columns -
// whether reading (scan, hydrate, eager-load), writing (insert, update,
// serialize), or enforcing policy (Fillable/Guarded mass-assignment) -
// goes through ModelMeta.
//
// Before this file existed, three different resolvers coexisted and drifted
// apart over time:
//
//   - fieldColumnName (model.go) for the write path used by structToMap.
//   - resolveColumnName (relation.go) for relation eager-load with the
//     primaryKey-shortcut handling.
//   - inline parsing in scanIntoStruct (query.go) for the read path that
//     additionally re-snake_cased the resolved column name, mangling
//     explicit `column:LegacyXYZ` tags into legacy_x_y_z (read failed
//     even though write succeeded).
//
// The recurring fix-by-fix pattern produced commits like
//
//	fix(orm): mapToStruct honors orm:"column:..."
//	fix(orm): serializeEmbedded honors orm:"column:..." on embedded base fields
//	fix(orm): respect caller-set CreatedAt
//	fix(orm): snake_case acronym boundaries
//
// Each landed in one of those resolvers without unifying the others.
//
// ModelMeta is the single source of truth: every callsite resolves columns,
// fillable/guarded sets, embedded-base fields, and the existence flag through
// it. Per-type metadata is computed once and cached in a sync.Map keyed by
// reflect.Type.
package orm

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ColumnDef describes one column of a model: the database column name
// (already snake_case'd or honoring an explicit orm:"column:..." tag), the
// reflect index path to walk into the value, the original Go field name
// (used for Fillable/Guarded lookups so policy keys on the field-name view
// even when the column has been renamed via tag), and a small set of
// classifier flags so callers don't have to re-parse the tag.
//
// IndexPath is non-empty and may have multiple entries when the field lives
// under one or more anonymous (embedded) struct fields. Use
// reflect.Value.FieldByIndex to navigate.
type ColumnDef struct {
	// Column is the SQL column name. Always non-empty for entries returned
	// by Columns(); empty values mean "skip" and are filtered out at meta
	// build time.
	Column string

	// FieldName is the Go struct field name (e.g. "RenamedField"), used
	// for Fillable/Guarded lookup so users key policy on the field-name
	// view (snake_case'd) regardless of orm:"column:..." renaming.
	FieldName string

	// FieldNameKey is toSnakeCase(FieldName); pre-computed because
	// mass-assignment lookups happen in hot paths.
	FieldNameKey string

	// IndexPath is the reflect.StructField index sequence to walk from
	// the root model to this field. Pass to reflect.Value.FieldByIndex.
	IndexPath []int

	// IsPrimaryKey, IsCreatedAt, IsUpdatedAt, IsDeletedAt are derived from
	// the orm tag (or, for embedded-base columns, from the well-known
	// field names CreatedAt/UpdatedAt/DeletedAt). Save paths use these
	// to skip auto-managed columns or stamp timestamps.
	IsPrimaryKey bool
	IsCreatedAt  bool
	IsUpdatedAt  bool
	IsDeletedAt  bool

	// IsJSON is true when the orm tag declares type:json or type:jsonb,
	// so write-path zero-value omission applies.
	IsJSON bool

	// IsAutoIncrement is true when the orm tag carries autoIncrement;
	// used by the save path to skip the ID on insert.
	IsAutoIncrement bool

	// FromEmbedded reports that this column came from an embedded ORM
	// base type (Model[T], UUIDModel[T], SoftDelete*, Immutable*).
	// serializeEmbedded uses this to apply embedded-specific zero rules.
	FromEmbedded bool
}

// ModelMeta holds the canonical reflection view of a model type. It is
// computed once per type and cached. Methods on *ModelMeta are safe for
// concurrent reads.
type ModelMeta struct {
	// Type is the underlying struct type (always Kind == reflect.Struct,
	// never a pointer).
	Type reflect.Type

	// columns is the ordered list of ColumnDef entries for every
	// persistent field reachable from Type, including those promoted
	// from embedded ORM base types. Excludes orm:"-", relation tags,
	// many-to-many virtual fields, and polymorphic morph fields (those
	// are handled by separate code paths and reach the resolver
	// indirectly via ColumnByName).
	columns []ColumnDef

	// byColumn / byField provide fast bidirectional lookup so callers
	// can resolve "field to column" or "column to field" in O(1) without
	// re-walking the type. Keyed on the user-facing names (Column for
	// byColumn, original FieldName for byField).
	byColumn map[string]int
	byField  map[string]int
}

// metaCache caches ModelMeta per concrete reflect.Type. sync.Map is used
// over a plain map+RWMutex because the access pattern is read-heavy and
// keys never change (a Go type's reflect.Type is a stable identity).
var metaCache sync.Map // map[reflect.Type]*ModelMeta

// MetaFor returns the ModelMeta for t, walking the type tree if not yet
// cached. t may be a struct, a pointer to a struct, or any nesting of
// those: the meta is always built for the underlying struct type so
// MetaFor(T) and MetaFor(*T) yield the same *ModelMeta pointer.
//
// Returns nil if t does not resolve to a struct (e.g. plain scalars,
// interfaces, slice elements that aren't structs). Callers should treat
// nil as "no model meta available" rather than panicking; the resolver
// is intentionally permissive so eager-load fallbacks can short-circuit
// without an extra type guard.
func MetaFor(t reflect.Type) *ModelMeta {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := metaCache.Load(t); ok {
		return cached.(*ModelMeta)
	}
	meta := buildMeta(t)
	actual, _ := metaCache.LoadOrStore(t, meta)
	return actual.(*ModelMeta)
}

// MetaForValue is a convenience over MetaFor for reflect.Value callers.
// Returns nil under the same conditions as MetaFor.
func MetaForValue(v reflect.Value) *ModelMeta {
	if !v.IsValid() {
		return nil
	}
	return MetaFor(v.Type())
}

// Columns returns the list of ColumnDef in declaration order. The slice
// is owned by the meta and must not be mutated by callers - treat as
// read-only. Iteration is allocation-free.
func (m *ModelMeta) Columns() []ColumnDef {
	if m == nil {
		return nil
	}
	return m.columns
}

// ColumnFor returns the database column name for a Go struct field name.
// Honors orm:"column:..." and falls back to snake_case of the field. Returns
// "" when the field is unknown, tagged orm:"-", or not persistent (relation,
// many-to-many, polymorphic).
func (m *ModelMeta) ColumnFor(fieldName string) string {
	if m == nil {
		return ""
	}
	if idx, ok := m.byField[fieldName]; ok {
		return m.columns[idx].Column
	}
	return ""
}

// FieldFor returns the Go field name for a database column name. Returns
// "" when the column is not declared by the model.
func (m *ModelMeta) FieldFor(column string) string {
	if m == nil {
		return ""
	}
	if idx, ok := m.byColumn[column]; ok {
		return m.columns[idx].FieldName
	}
	return ""
}

// ColumnByName looks up a ColumnDef by column name. Returns the def and
// true on hit, zero value and false on miss. Used by mapToStruct to
// navigate to the target field via IndexPath.
func (m *ModelMeta) ColumnByName(column string) (ColumnDef, bool) {
	if m == nil {
		return ColumnDef{}, false
	}
	if idx, ok := m.byColumn[column]; ok {
		return m.columns[idx], true
	}
	return ColumnDef{}, false
}

// ColumnByField looks up a ColumnDef by Go field name. Mirror of
// ColumnByName for callers that already hold a field name.
func (m *ModelMeta) ColumnByField(fieldName string) (ColumnDef, bool) {
	if m == nil {
		return ColumnDef{}, false
	}
	if idx, ok := m.byField[fieldName]; ok {
		return m.columns[idx], true
	}
	return ColumnDef{}, false
}

// ----------------------------------------------------------------------------
// Builder
// ----------------------------------------------------------------------------

// isEmbeddedBaseType reports whether the field is an embedded ORM
// trait or convenience composition. A field qualifies when it's
// anonymous and its type lives in this package. The prior
// prefix-string match locked the framework to a hardcoded list of six
// type names; pkg-path comparison via isFrameworkType (composition.go)
// lets arbitrary trait compositions (orm.IDInt[T] + orm.Timestamps + ...)
// participate as embedded base fields without code changes.
func isEmbeddedBaseType(field reflect.StructField) bool {
	if !field.Anonymous {
		return false
	}
	return isFrameworkType(field.Type)
}

// buildMeta walks t once and produces a ModelMeta. Pure function: no I/O,
// no allocations on the hot path post-build. Idempotent: calling it
// twice on the same type returns equivalent metas.
//
// Walk order:
//  1. For each direct (non-anonymous) exported field, emit a ColumnDef.
//     Skip orm:"-", relation tags, many-to-many, and polymorphic.
//  2. For each anonymous embedded ORM base field, recurse into its fields
//     and emit them as columns marked FromEmbedded=true. Field shadowing
//     is honoured: if an outer field shares a name with an embedded one,
//     the outer wins (matches Go's promotion rules and serializeEmbedded).
//  3. For other anonymous structs (non-base embeds), recurse with the
//     same field-name shadowing rules.
func buildMeta(t reflect.Type) *ModelMeta {
	meta := &ModelMeta{
		Type:     t,
		byColumn: make(map[string]int),
		byField:  make(map[string]int),
	}

	// Pre-scan the outer struct's directly-declared field names so the
	// embedded-walk can suppress shadowed fields. This matches Go's
	// promotion semantics: the outer declaration always wins.
	outerFieldNames := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() && !f.Anonymous {
			outerFieldNames[f.Name] = true
		}
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		path := []int{i}

		// Embedded ORM base type: walk its fields, mark FromEmbedded.
		if isEmbeddedBaseType(field) {
			meta.appendEmbedded(field, path, outerFieldNames)
			continue
		}

		// Other anonymous structs (non-time.Time): recurse so their
		// promoted fields participate. time.Time is a special case:
		// it's a struct but conceptually a scalar value.
		if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Type.String() != "time.Time" {
			meta.appendAnonymous(field, path, outerFieldNames)
			continue
		}

		col := buildColumnDef(field, path, false)
		if col.Column == "" {
			continue
		}
		meta.appendColumn(col)
	}

	return meta
}

// appendColumn registers a ColumnDef and updates the lookup indices.
// First-write-wins for both byColumn and byField so embedded fields
// don't shadow already-emitted outer ones if the build order is wrong
// (defensive: buildMeta itself orders correctly).
func (m *ModelMeta) appendColumn(col ColumnDef) {
	idx := len(m.columns)
	m.columns = append(m.columns, col)
	if _, exists := m.byColumn[col.Column]; !exists {
		m.byColumn[col.Column] = idx
	}
	if _, exists := m.byField[col.FieldName]; !exists {
		m.byField[col.FieldName] = idx
	}
}

// appendEmbedded walks an embedded ORM base type (or trait composition)
// and registers each of its exported fields as a column with
// FromEmbedded=true. Anonymous embedded sub-fields (e.g. Model[T] embeds
// IDInt[T]+Timestamps+Existence) are recursed into so traits at any
// nesting depth contribute their columns. Honors field shadowing: any
// embedded field whose name appears in shadowedByOuter is skipped
// because Go's field promotion gives the outer declaration priority.
func (m *ModelMeta) appendEmbedded(parent reflect.StructField, parentPath []int, shadowedByOuter map[string]bool) {
	embeddedType := parent.Type
	if embeddedType.Kind() == reflect.Ptr {
		embeddedType = embeddedType.Elem()
	}
	if embeddedType.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < embeddedType.NumField(); i++ {
		f := embeddedType.Field(i)
		if !f.IsExported() {
			continue
		}
		if shadowedByOuter[f.Name] {
			continue
		}
		path := append(append([]int{}, parentPath...), i)

		// Recurse into anonymous embeds so trait compositions
		// (Model[T] embeds IDInt[T]+Timestamps+Existence) flatten.
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Type.String() != "time.Time" {
			if isFrameworkType(f.Type) {
				m.appendEmbedded(f, path, shadowedByOuter)
			} else {
				m.appendAnonymous(f, path, shadowedByOuter)
			}
			continue
		}

		col := buildColumnDef(f, path, true)
		if col.Column == "" {
			continue
		}
		m.appendColumn(col)
	}
}

// appendAnonymous walks a non-base anonymous struct embed (e.g. an
// app-level base type that itself embeds Model[T] plus app columns).
// Recurses one level: nested embeddings are handled by the recursive
// call, which itself may delegate to appendEmbedded when it hits a base
// type prefix.
func (m *ModelMeta) appendAnonymous(parent reflect.StructField, parentPath []int, shadowedByOuter map[string]bool) {
	innerType := parent.Type
	if innerType.Kind() == reflect.Ptr {
		innerType = innerType.Elem()
	}
	if innerType.Kind() != reflect.Struct {
		return
	}
	innerOuterFieldNames := make(map[string]bool, innerType.NumField())
	for i := 0; i < innerType.NumField(); i++ {
		f := innerType.Field(i)
		if f.IsExported() && !f.Anonymous {
			innerOuterFieldNames[f.Name] = true
		}
	}
	for i := 0; i < innerType.NumField(); i++ {
		f := innerType.Field(i)
		if !f.IsExported() {
			continue
		}
		if shadowedByOuter[f.Name] {
			continue
		}
		path := append(append([]int{}, parentPath...), i)
		if isEmbeddedBaseType(f) {
			m.appendEmbedded(f, path, innerOuterFieldNames)
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Type.String() != "time.Time" {
			m.appendAnonymous(f, path, innerOuterFieldNames)
			continue
		}
		col := buildColumnDef(f, path, false)
		if col.Column == "" {
			continue
		}
		m.appendColumn(col)
	}
}

// buildColumnDef extracts column metadata from a single struct field.
// Returns a zero-Column ColumnDef when the field is not persistent
// (orm:"-", relation tag, many-to-many, polymorphic morph). Callers
// must skip such entries.
func buildColumnDef(field reflect.StructField, path []int, fromEmbedded bool) ColumnDef {
	tag := field.Tag.Get("orm")

	// Skip orm:"-" and relation kinds outright. These are the same skip
	// rules every legacy resolver applied; centralising them here means
	// future "kind" tags (e.g. "computed:") only need to be added once.
	if tag == "-" {
		return ColumnDef{}
	}
	if strings.Contains(tag, "relation:") {
		return ColumnDef{}
	}
	if extractManyToManyValue(tag) != "" {
		return ColumnDef{}
	}
	if extractPolymorphicValue(tag) != "" {
		return ColumnDef{}
	}

	column := columnNameFromTag(tag)
	if column == "" {
		// primaryKey-without-explicit-column shortcut: matches the
		// legacy resolveColumnName semantics so a field declared as
		// `orm:"primaryKey;autoIncrement"` (or just `orm:"primaryKey"`)
		// always lands on "id" regardless of its Go name. The stock
		// embedded base types name their PK field "ID" so snake_case
		// already produces "id"; this branch covers app-level keys
		// like `MyKey uint`orm:"primaryKey"` that want the convention.
		if hasTagPart(tag, "primaryKey") {
			column = "id"
		} else {
			column = ToSnakeCase(field.Name)
		}
	}

	def := ColumnDef{
		Column:       column,
		FieldName:    field.Name,
		FieldNameKey: ToSnakeCase(field.Name),
		IndexPath:    path,
		FromEmbedded: fromEmbedded,
		IsJSON:       isJSONColumn(tag),
	}

	// Tag-driven flags. Each is independent so a single tag can declare
	// multiple roles (e.g. a primaryKey that's also autoIncrement).
	for _, part := range splitTagParts(tag) {
		switch {
		case part == "primaryKey":
			def.IsPrimaryKey = true
		case part == "autoIncrement":
			def.IsAutoIncrement = true
		case part == "autoCreateTime":
			def.IsCreatedAt = true
		case part == "autoUpdateTime":
			def.IsUpdatedAt = true
		}
	}

	// Field-name fallbacks for the well-known timestamp columns. The
	// stock embedded base types declare `orm:"autoCreateTime"` etc., so
	// the tag-driven branch above already catches them. The fallback
	// covers app-level models that name their fields CreatedAt/etc.
	// without the directive, keeping per-column behaviour predictable
	// regardless of whether the user remembered the tag.
	if !def.IsCreatedAt && field.Name == "CreatedAt" {
		def.IsCreatedAt = true
	}
	if !def.IsUpdatedAt && field.Name == "UpdatedAt" {
		def.IsUpdatedAt = true
	}
	if !def.IsDeletedAt && field.Name == "DeletedAt" {
		def.IsDeletedAt = true
	}

	return def
}

// columnNameFromTag returns the explicit `column:...` directive value
// from an orm tag, or "" when no such directive is present. Honors the
// ';' separator, ignores other directives, and trims whitespace so tags
// formatted with spaces (e.g. "primaryKey; column:foo") still resolve.
//
// Critically, the returned name is NOT re-snake_cased: the legacy
// scanIntoStruct path mangled `column:LegacyXYZ` into legacy_x_y_z and
// silently broke read-back of column-tagged fields. The canonical rule
// is: explicit tag wins verbatim, fallback uses snake_case.
func columnNameFromTag(tag string) string {
	if tag == "" {
		return ""
	}
	for _, part := range splitTagParts(tag) {
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}

// splitTagParts splits an orm tag on ';' and trims whitespace from each
// segment. A small helper but used in three places so worth extracting.
func splitTagParts(tag string) []string {
	if tag == "" {
		return nil
	}
	raw := strings.Split(tag, ";")
	out := raw[:0]
	for _, p := range raw {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// hasTagPart reports whether the orm tag carries the named directive
// either bare (e.g. "primaryKey") or as a key:value head (e.g.
// "primaryKey:auto"). Whitespace is tolerated.
func hasTagPart(tag, name string) bool {
	for _, part := range splitTagParts(tag) {
		if part == name {
			return true
		}
		if strings.HasPrefix(part, name+":") {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// Mass-assignment policy
// ----------------------------------------------------------------------------

// FillablePolicy holds the resolved fillable allowlist and guarded
// denylist for a given model instance. Built once per write call so the
// downstream loop can answer "is this column writable?" in O(1).
//
// Both maps key on the snake_case'd Go field name. Users declare
// policies via the Fillable()/Guarded() interfaces, and the framework
// guarantees that policy is enforced regardless of whether the inbound
// payload uses field names or column names. This is the security
// invariant: an attacker cannot bypass a guard by submitting the
// column-tag value instead of the snake_case field name.
type FillablePolicy struct {
	HasFillable bool
	HasGuarded  bool
	Fillable    map[string]bool
	Guarded     map[string]bool
}

// StrictMassAssignment is an opt-in compatibility escape hatch for models
// that want deny-by-default mass-assignment behavior without declaring a
// Fillable() allowlist. When StrictMassAssignment() returns true and the model
// declares neither Fillable() nor Guarded(), PolicyFor resolves the model as an
// empty Fillable allowlist: map-based mass assignment denies every application
// field unless another policy is declared.
type StrictMassAssignment interface {
	StrictMassAssignment() bool
}

// PolicyFor extracts mass-assignment policy from a model instance.
//
// Compatibility warning: when a model declares neither Fillable() nor
// Guarded(), PolicyFor returns a zero FillablePolicy. That zero policy is
// mass-assignment-open: FillablePolicy.Allows returns true for every column
// writable from a map. This affects Query[T].Create(map),
// Query[T].Update(map), Model[T].Update, and FirstOrCreate/UpdateOrCreate.
// Treat this as a security footgun for models that receive user-controlled
// maps because sensitive persisted columns such as role or is_admin are
// writable by default.
//
// Any model used with map-based Create/Update should declare Fillable()
// (allowlist, deny-by-default) or Guarded() (denylist). As an opt-in safer
// default that preserves the package's permissive zero-policy compatibility,
// a model may implement StrictMassAssignment and return true; if it declares
// neither Fillable() nor Guarded(), PolicyFor resolves it as an empty Fillable
// allowlist so all application fields are denied by default.
func PolicyFor(s any) FillablePolicy {
	p := FillablePolicy{}
	if f, ok := s.(Fillable); ok {
		set := make(map[string]bool, len(f.Fillable()))
		for _, name := range f.Fillable() {
			set[name] = true
		}
		p.Fillable = set
		p.HasFillable = true
	}
	if g, ok := s.(Guarded); ok {
		set := make(map[string]bool, len(g.Guarded()))
		for _, name := range g.Guarded() {
			set[name] = true
		}
		p.Guarded = set
		p.HasGuarded = true
	}
	if !p.HasFillable && !p.HasGuarded {
		if strict, ok := s.(StrictMassAssignment); ok && strict.StrictMassAssignment() {
			p.Fillable = map[string]bool{}
			p.HasFillable = true
		}
	}
	return p
}

var (
	massAssignmentWarnerMu sync.RWMutex
	massAssignmentWarner   func(modelType string)
	massAssignmentWarnOnce sync.Map // modelType -> struct{}, so each type warns once
)

// SetMassAssignmentWarner installs a callback invoked the first time a model
// declaring neither Fillable() nor Guarded() (nor opting into
// StrictMassAssignment) is used with map-based Create/Update. The zero policy
// is mass-assignment-open, so this surfaces the silently-insecure default
// without changing behaviour. nil (the default) disables the warning; the
// framework wires it to its logger at startup. Safe for concurrent use.
func SetMassAssignmentWarner(fn func(modelType string)) {
	massAssignmentWarnerMu.Lock()
	massAssignmentWarner = fn
	massAssignmentWarnerMu.Unlock()
}

// warnOpenMassAssignment fires the configured warner once per model type. It is
// called only from the map-based assignment path, where an open policy means
// arbitrary client keys can set arbitrary columns.
func warnOpenMassAssignment(s any) {
	massAssignmentWarnerMu.RLock()
	fn := massAssignmentWarner
	massAssignmentWarnerMu.RUnlock()
	if fn == nil {
		return
	}
	t := fmt.Sprintf("%T", s)
	if _, loaded := massAssignmentWarnOnce.LoadOrStore(t, struct{}{}); loaded {
		return
	}
	fn(t)
}

// Allows reports whether the policy permits writing to fieldNameKey
// (the snake_case'd Go field name).
//
// Compatibility warning: when neither Fillable() nor Guarded() is declared,
// the zero FillablePolicy is mass-assignment-open and Allows returns true for
// every field. Query[T].Create(map), Query[T].Update(map), Model[T].Update,
// and FirstOrCreate/UpdateOrCreate will therefore accept arbitrary map keys
// that resolve to persisted columns, including sensitive names such as role or
// is_admin. Models used with user-controlled map-based Create/Update should
// declare Fillable() (allowlist, deny-by-default), Guarded() (denylist), or
// opt in to StrictMassAssignment.
func (p FillablePolicy) Allows(fieldNameKey string) bool {
	if p.HasFillable && !p.Fillable[fieldNameKey] {
		return false
	}
	if p.HasGuarded && p.Guarded[fieldNameKey] {
		return false
	}
	return true
}

// ----------------------------------------------------------------------------
// Existence flag (IsExisting)
// ----------------------------------------------------------------------------

// MarkExists sets the IsExisting flag for a freshly-loaded model so a
// subsequent Save routes through UPDATE instead of re-inserting.
// Routes through the package-level side-channel (existence.go); the
// previous typed-receiver interface (existenceSetter) is gone with the
// Existence trait drop.
//
// This is the canonical entry point for read paths: Query[T].Get,
// RawQuery.First/Get, relation eager-load, polymorphic eager-load,
// many-to-many eager-load, and Morph.Resolve all funnel through here.
func MarkExists[T any](model *T) {
	if model == nil {
		return
	}
	markModelExisting(model)
}

// MarkExistsValue is the reflect.Value variant for callers (relation
// eager-load, M2M, polymorphic) that don't have a typed *T handle.
// v must be addressable; non-addressable values are silently ignored.
func MarkExistsValue(v reflect.Value) {
	if !v.IsValid() || !v.CanAddr() {
		return
	}
	storeExistenceBitFromAny(v.Addr().Interface())
}

// IsExistingValue reports whether the IsExisting flag is set on a
// model's embedded base. Returns false when v isn't a struct, doesn't
// embed a base type, or doesn't carry the flag. Used by tests and by
// the save path's branch decision.
func IsExistingValue(v reflect.Value) bool {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	// Walk fields looking for the embedded base. A direct FieldByName
	// would catch "IsExisting" promoted through one level of embedding;
	// for nested embeddings we walk explicitly.
	if f := v.FieldByName("IsExisting"); f.IsValid() && f.Kind() == reflect.Bool {
		return f.Bool()
	}
	return false
}
