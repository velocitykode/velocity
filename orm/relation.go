package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/velocitykode/velocity/orm/drivers"
)

// buildScopedInSelect compiles a "SELECT * FROM table WHERE col IN (...)
// AND (<scope conditions>)" statement for the related type t via the
// grammar's CompileSelect. Used by the eager-load helpers in
// relation.go, relation_m2m.go, relation_polymorphic.go, and morph.go
// so the IN query honours every global scope registered on t (tenant,
// archive, locale, state, soft-delete, ...).
//
// The scope conditions are wrapped in a single parenthesized group
// before being AND-joined to the IN predicate. This preserves the
// scope's internal AND/OR composition (e.g. a scope written as
// q.Where("tenant_id = ?", t).OrWhere("public = ?", true) compiles to
// "id IN (...) AND (tenant_id = ? OR public = ?)"). Coercing every
// scope condition's Type to "and" before appending would silently
// rewrite that to "id IN (...) AND tenant_id = ? AND public = ?",
// narrowing the matched set and changing correctness.
//
// When no constructor is registered for t (no AddGlobalScope[T] or
// newQuery[T] has ever fired for this T), the helper falls back to
// the legacy hand-rolled predicate: the IN clause plus, when t embeds
// SoftDeletes, a deleted_at IS NULL filter. The legacy fallback
// preserves the historical behaviour for callers that have not opted
// into the global-scope primitive at all.
//
// Returns (sql, args, nil) on success or (zero, zero, err) when
// applyGlobalScopesByType surfaces a scope validation error. Callers
// MUST propagate the error rather than execute SQL with the scope
// silently dropped.
func buildScopedInSelect(ctx context.Context, driver drivers.Driver, t reflect.Type, table, fkColumn string, ids []any) (string, []any, error) {
	grammar := driver.Grammar()
	scopeConditions, err := applyGlobalScopesByType(ctx, t, driver)
	if err != nil {
		return "", nil, err
	}

	if scopeConditions == nil {
		// Fall back to the legacy hand-rolled predicate. We still
		// embed deleted_at IS NULL for soft-delete models so callers
		// that have never registered any global scope keep their
		// previous behaviour. Callers who opt in via AddGlobalScope
		// (or by triggering newQuery[T]) hit the grammar-compiled
		// path above instead.
		placeholders := make([]string, len(ids))
		for i := range ids {
			placeholders[i] = grammar.Placeholder(i + 1)
		}
		sqlStr := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
			grammar.QuoteIdentifier(table),
			grammar.QuoteIdentifier(fkColumn),
			strings.Join(placeholders, ", "),
		)
		if checkSoftDelete(t) {
			sqlStr += " AND " + grammar.QuoteIdentifier("deleted_at") + " IS NULL"
		}
		return sqlStr, ids, nil
	}

	// First condition: the relation IN predicate. Always AND-joined at
	// the top level; this is the eager-load contract.
	conditions := make([]drivers.Condition, 0, 2)
	conditions = append(conditions, drivers.Condition{
		Column:   fkColumn,
		Operator: "IN",
		Value:    ids,
		Type:     "and",
	})
	// Second condition (only when scopes are present): one grouped
	// block carrying the harvested scope conditions verbatim. The
	// group's outer Type is "and" so it AND-joins to the IN predicate,
	// but the inner conditions keep their original And/Or types so a
	// scope that used OrWhere stays an OR at the same precedence level.
	if len(scopeConditions) > 0 {
		conditions = append(conditions, drivers.Condition{
			Type:  "and",
			Group: scopeConditions,
		})
	}
	selectQuery := &drivers.SelectQuery{
		Table:      table,
		Columns:    []string{"*"},
		Conditions: conditions,
	}
	sqlStr, args := grammar.CompileSelect(selectQuery)
	return sqlStr, args, nil
}

// RelationType represents the type of a model relationship.
type RelationType int

const (
	// HasOne defines a one-to-one relationship where the related model
	// holds the foreign key. Example: User has one Profile.
	HasOne RelationType = iota

	// HasMany defines a one-to-many relationship where many related models
	// hold the foreign key. Example: User has many Posts.
	HasMany

	// BelongsTo defines the inverse of HasOne/HasMany where this model
	// holds the foreign key. Example: Post belongs to User.
	BelongsTo
)

// relationMeta holds parsed metadata about a relationship field.
type relationMeta struct {
	relType      RelationType
	fieldName    string       // Go struct field name (e.g. "Posts")
	fieldIndex   int          // Index in the parent struct
	foreignKey   string       // Foreign key column name (snake_case)
	localKey     string       // Local/owner key column name (snake_case)
	relatedType  reflect.Type // The related Go struct type (e.g. Post, not *Post or []Post)
	relatedTable string       // Resolved table name for the related type
	isSlice      bool         // Field is []T or []*T
	isPtr        bool         // Element is *T (slices) or field is *T (single)
}

// parseRelationTag parses a relation tag value.
// Format: "hasOne,foreignKey,localKey" or "hasMany,foreignKey,localKey" or "belongsTo,foreignKey,localKey"
func parseRelationTag(value string) (RelationType, string, string, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return 0, "", "", fmt.Errorf("orm: invalid relation tag %q — expected \"type,foreignKey,localKey\"", value)
	}

	var relType RelationType
	switch strings.TrimSpace(parts[0]) {
	case "hasOne":
		relType = HasOne
	case "hasMany":
		relType = HasMany
	case "belongsTo":
		relType = BelongsTo
	default:
		return 0, "", "", fmt.Errorf("orm: unknown relation type %q", parts[0])
	}

	fk := strings.TrimSpace(parts[1])
	lk := strings.TrimSpace(parts[2])

	if fk == "" || lk == "" {
		return 0, "", "", fmt.Errorf("orm: relation tag %q has empty key names", value)
	}

	return relType, fk, lk, nil
}

// resolveRelationMeta extracts relation metadata for a named preload on the given model type.
func resolveRelationMeta(modelType reflect.Type, preloadName string) (*relationMeta, error) {
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	field, fieldIndex, found := findRelationField(modelType, preloadName)
	if !found {
		return nil, fmt.Errorf("orm: relation %q not found on %s", preloadName, modelType.Name())
	}

	tag := field.Tag.Get("orm")
	relValue := extractRelationValue(tag)
	if relValue == "" {
		return nil, fmt.Errorf("orm: field %q on %s does not have a relation tag", field.Name, modelType.Name())
	}

	relType, fk, lk, err := parseRelationTag(relValue)
	if err != nil {
		return nil, err
	}

	if err := validateIdentifier(fk); err != nil {
		return nil, fmt.Errorf("orm: invalid foreign key in relation tag: %w", err)
	}
	if err := validateIdentifier(lk); err != nil {
		return nil, fmt.Errorf("orm: invalid local key in relation tag: %w", err)
	}

	// Determine the related type from the field's Go type
	fieldType := field.Type
	isSlice := false
	isPtr := false

	switch fieldType.Kind() {
	case reflect.Slice:
		isSlice = true
		elemType := fieldType.Elem()
		if elemType.Kind() == reflect.Ptr {
			isPtr = true
			fieldType = elemType.Elem()
		} else {
			fieldType = elemType
		}
	case reflect.Ptr:
		isPtr = true
		fieldType = fieldType.Elem()
	}

	if fieldType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: relation field %q must be a struct, pointer to struct, or slice of structs", field.Name)
	}

	tableName := resolveTableNameReflect(fieldType)
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("orm: invalid table name for related type %s: %w", fieldType.Name(), err)
	}

	return &relationMeta{
		relType:      relType,
		fieldName:    field.Name,
		fieldIndex:   fieldIndex,
		foreignKey:   fk,
		localKey:     lk,
		relatedType:  fieldType,
		relatedTable: tableName,
		isSlice:      isSlice,
		isPtr:        isPtr,
	}, nil
}

// findRelationField finds a struct field by name that has a relation tag.
// Tries exact match first, then case-insensitive.
func findRelationField(modelType reflect.Type, name string) (reflect.StructField, int, bool) {
	// Exact match
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if field.Name == name && strings.Contains(field.Tag.Get("orm"), "relation:") {
			return field, i, true
		}
	}

	// Case-insensitive match
	lowerName := strings.ToLower(name)
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if strings.ToLower(field.Name) == lowerName && strings.Contains(field.Tag.Get("orm"), "relation:") {
			return field, i, true
		}
	}

	return reflect.StructField{}, 0, false
}

// extractRelationValue extracts the relation value from an orm tag.
// e.g., "relation:hasMany,user_id,id" → "hasMany,user_id,id"
func extractRelationValue(tag string) string {
	parts := strings.Split(tag, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "relation:") {
			return strings.TrimPrefix(part, "relation:")
		}
	}
	return ""
}

// resolveTableNameReflect resolves the table name for a type using
// reflection. Delegates to the canonical deriveTableName so the relation
// resolver agrees with the read and write paths; preserves the historical
// contract of returning "" for an unnamed type.
func resolveTableNameReflect(t reflect.Type) string {
	return deriveTableName(t)
}

// getFieldValueByColumn extracts the value of a struct field matching the
// given column name, walking into embedded structs (notably the framework
// base types) so the eager-load helpers can address columns like "id" or
// "created_at" that live on Model[T] rather than the outer struct.
//
// Implemented on top of the canonical ModelMeta so the column resolution
// matches structToMap/mapToStruct exactly. Without this, an eager-load
// that joins on a column-tagged field (orm:"column:legacy_xyz") would
// look up the wrong column and silently return zero matches.
func getFieldValueByColumn(v reflect.Value, columnName string) (any, bool) {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, false
	}
	meta := MetaForValue(v)
	if meta == nil {
		return nil, false
	}
	col, ok := meta.ColumnByName(columnName)
	if !ok {
		return nil, false
	}
	fv := v.FieldByIndex(col.IndexPath)
	if !fv.IsValid() {
		return nil, false
	}
	// Dereference a pointer field (a nullable FK/key modelled as e.g. *uint) to
	// its underlying value: relation eager-loading groups parent and child keys
	// through a map[any], where a *uint address never equals the parent's uint
	// value, so a nullable FK would silently load nothing. A nil pointer is a
	// null key and matches no parent.
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return nil, false
		}
		fv = fv.Elem()
	}
	return fv.Interface(), true
}

// resolveColumnName determines the database column name for a struct field.
// Thin shim over the canonical reflection resolver kept for the relation
// package's tests and for any external callers; new code should reach the
// resolver directly via MetaFor(t).ColumnFor(field.Name) or buildColumnDef.
//
// Honors orm:"-" (returns ""), orm:"column:..." (verbatim), and the
// primaryKey-without-column shortcut (returns "id"). Falls back to
// snake_case of the Go field name. Identical semantics to the inline
// handling in buildColumnDef so this wrapper can be retired once callers
// migrate.
func resolveColumnName(field reflect.StructField) string {
	tag := field.Tag.Get("orm")
	if tag == "-" {
		return ""
	}
	if strings.Contains(tag, "relation:") {
		return ""
	}
	if extractManyToManyValue(tag) != "" {
		return ""
	}
	if extractPolymorphicValue(tag) != "" {
		return ""
	}
	if name := columnNameFromTag(tag); name != "" {
		return name
	}
	if hasTagPart(tag, "primaryKey") {
		return "id"
	}
	return ToSnakeCase(field.Name)
}

// normalizeKey converts numeric types to int64 for consistent map key comparison.
// String keys (UUIDs) are returned as-is.
func normalizeKey(v any) any {
	switch val := v.(type) {
	case uint:
		return int64(val)
	case uint8:
		return int64(val)
	case uint16:
		return int64(val)
	case uint32:
		return int64(val)
	case uint64:
		return int64(val)
	case int:
		return int64(val)
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	case int64:
		return val
	default:
		return v
	}
}

// isZeroKey returns true if the value represents a zero/empty key that should be skipped.
func isZeroKey(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case int64:
		return val == 0
	case string:
		return val == ""
	default:
		return false
	}
}

// markIsExisting sets the IsExisting flag for a freshly-loaded model
// via the package-level side-channel (existence.go). A row read via an
// eager-loaded relation and a row read directly produce the same
// IsExisting value, regardless of the trait composition.
//
// v must be addressable (callers all derive it from reflect.New
// followed by .Elem()).
func markIsExisting(v reflect.Value) {
	if !v.CanAddr() {
		return
	}
	storeExistenceBitFromAny(v.Addr().Interface())
}

// loadRelations implements eager loading for preloaded relationships.
// Called by Query[T].Get() after the primary query results are loaded.
//
// Dispatches by tag prefix on the named field:
//   - relation:    -> hasOne/hasMany/belongsTo via loadRelation
//   - manyToMany:  -> many-to-many pivot via loadM2M
//   - polymorphic: -> morph batched per type via loadPolymorphic
func (q *Query[T]) loadRelations(ctx context.Context, models *[]T) error {
	if len(*models) == 0 {
		return nil
	}

	modelType := reflect.TypeOf((*T)(nil)).Elem()
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	for _, preload := range q.preloads {
		// Probe for the field once and dispatch to the correct loader.
		if field, ok := lookupTaggedField(modelType, preload); ok {
			tag := field.Tag.Get("orm")
			switch {
			case extractManyToManyValue(tag) != "":
				meta, err := resolveManyToManyMeta(modelType, preload)
				if err != nil {
					return err
				}
				if err := q.loadM2M(ctx, models, meta); err != nil {
					return err
				}
				continue
			case extractPolymorphicValue(tag) != "":
				meta, err := resolvePolymorphicMeta(modelType, preload)
				if err != nil {
					return err
				}
				if err := q.loadPolymorphic(ctx, models, meta); err != nil {
					return err
				}
				continue
			}
		}
		// Fall through to the legacy "relation:" loader.
		meta, err := resolveRelationMeta(modelType, preload)
		if err != nil {
			return err
		}
		if err := q.loadRelation(ctx, models, meta); err != nil {
			return err
		}
	}
	return nil
}

// lookupTaggedField returns a struct field by name (exact then case-insensitive)
// that carries any of the relation-style orm tags (relation:, manyToMany:,
// polymorphic:). Returns ok=false when no matching field is present.
func lookupTaggedField(modelType reflect.Type, name string) (reflect.StructField, bool) {
	hasRel := func(tag string) bool {
		return strings.Contains(tag, "relation:") ||
			extractManyToManyValue(tag) != "" ||
			extractPolymorphicValue(tag) != ""
	}
	for i := 0; i < modelType.NumField(); i++ {
		f := modelType.Field(i)
		if f.Name == name && hasRel(f.Tag.Get("orm")) {
			return f, true
		}
	}
	lower := strings.ToLower(name)
	for i := 0; i < modelType.NumField(); i++ {
		f := modelType.Field(i)
		if strings.ToLower(f.Name) == lower && hasRel(f.Tag.Get("orm")) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// loadRelation loads a single relationship for all parent models using a single IN query.
//
// Scope semantics: the SELECT against the related table runs every
// global scope registered for meta.relatedType (tenant, archive,
// locale, state, soft-delete, ...). The previous implementation
// hand-rolled "SELECT * FROM tbl WHERE fk IN (...)" and inlined a
// deleted_at IS NULL fallback for soft-delete models; every other
// scope was silently dropped. Routing the IN predicate plus the
// scope conditions through the grammar's CompileSelect keeps the
// eager-load path symmetric with the typed Query[T] path.
func (q *Query[T]) loadRelation(ctx context.Context, models *[]T, meta *relationMeta) error {
	// Determine which column to collect from parents and which to query on the related table.
	// HasOne/HasMany: collect parent's localKey, query related's foreignKey
	// BelongsTo: collect parent's foreignKey, query related's localKey
	var collectColumn, queryColumn string
	switch meta.relType {
	case HasOne, HasMany:
		collectColumn = meta.localKey
		queryColumn = meta.foreignKey
	case BelongsTo:
		collectColumn = meta.foreignKey
		queryColumn = meta.localKey
	}

	// 1. Collect unique key values from parent models
	seen := make(map[any]bool)
	var keys []any
	for i := range *models {
		v := reflect.ValueOf(&(*models)[i]).Elem()
		val, ok := getFieldValueByColumn(v, collectColumn)
		if !ok {
			continue
		}
		normalized := normalizeKey(val)
		if isZeroKey(normalized) {
			continue
		}
		if !seen[normalized] {
			seen[normalized] = true
			keys = append(keys, val)
		}
	}

	if len(keys) == 0 {
		return nil
	}

	// 2. Build the IN predicate plus any scope conditions registered on
	// the related model, then compile through the grammar so we honour
	// every dialect's placeholder/quote conventions. A scope that fails
	// validation surfaces here; propagate the error rather than execute
	// SQL with the scope silently dropped.
	relSQL, sqlArgs, scopeErr := buildScopedInSelect(ctx, q.driver, meta.relatedType, meta.relatedTable, queryColumn, keys)
	if scopeErr != nil {
		return fmt.Errorf("orm: failed to apply scopes for relation %q: %w", meta.fieldName, scopeErr)
	}

	rows, err := q.driver.QueryContext(ctx, relSQL, sqlArgs...)
	if err != nil {
		return fmt.Errorf("orm: failed to load relation %q: %w", meta.fieldName, err)
	}
	defer rows.Close()

	// 3. Scan results and group by the query column value. The scan plan
	// is resolved once for the whole result set (lazily, so an empty
	// result set never touches rows.Columns).
	groups := make(map[any][]reflect.Value)
	var plan *scanPlan
	for rows.Next() {
		if plan == nil {
			var perr error
			if plan, perr = newScanPlan(rows, meta.relatedType); perr != nil {
				return fmt.Errorf("orm: failed to scan relation %q: %w", meta.fieldName, perr)
			}
		}
		ptr := reflect.New(meta.relatedType)
		if err := plan.scanRow(rows, ptr.Elem()); err != nil {
			return fmt.Errorf("orm: failed to scan relation %q: %w", meta.fieldName, err)
		}
		elem := ptr.Elem()
		markIsExisting(elem)

		groupKey, ok := getFieldValueByColumn(elem, queryColumn)
		if !ok {
			continue
		}
		normalized := normalizeKey(groupKey)
		groups[normalized] = append(groups[normalized], elem)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("orm: error iterating relation %q results: %w", meta.fieldName, err)
	}

	// 4. Assign results back to parent models
	for i := range *models {
		parentVal := reflect.ValueOf(&(*models)[i]).Elem()
		collectVal, ok := getFieldValueByColumn(parentVal, collectColumn)
		if !ok {
			continue
		}
		normalized := normalizeKey(collectVal)
		matches := groups[normalized]

		relField := parentVal.Field(meta.fieldIndex)

		switch meta.relType {
		case HasMany:
			assignSlice(relField, matches, meta)
		case HasOne, BelongsTo:
			assignSingle(relField, matches, meta)
		}
	}

	return nil
}

// assignSlice sets a slice field (HasMany) from matched related models.
// Re-marks each final slice element so the side-channel existence
// store sees the caller-visible pointer (slice element address for
// value-type slices, the held pointer for pointer-type slices).
func assignSlice(field reflect.Value, matches []reflect.Value, meta *relationMeta) {
	if len(matches) == 0 {
		return
	}
	slice := reflect.MakeSlice(field.Type(), len(matches), len(matches))
	for j, m := range matches {
		if meta.isPtr {
			ptr := reflect.New(meta.relatedType)
			ptr.Elem().Set(m)
			slice.Index(j).Set(ptr)
		} else {
			slice.Index(j).Set(m)
		}
	}
	field.Set(slice)
	// Mark final addresses now that the slice is in place. For value
	// slices, use field.Index(j) which is addressable in the parent
	// struct; for pointer slices, dereference the stored pointer.
	for j := 0; j < field.Len(); j++ {
		elem := field.Index(j)
		if meta.isPtr {
			if elem.IsNil() {
				continue
			}
			storeExistenceBitFromAny(elem.Interface())
		} else if elem.CanAddr() {
			storeExistenceBitFromAny(elem.Addr().Interface())
		}
	}
}

// assignSingle sets a single-value field (HasOne/BelongsTo) from the first match.
// Re-marks the final destination so the caller-visible pointer
// participates in the existence store.
func assignSingle(field reflect.Value, matches []reflect.Value, meta *relationMeta) {
	if len(matches) == 0 {
		return
	}
	if meta.isPtr {
		ptr := reflect.New(meta.relatedType)
		ptr.Elem().Set(matches[0])
		field.Set(ptr)
		storeExistenceBitFromAny(ptr.Interface())
	} else {
		field.Set(matches[0])
		if field.CanAddr() {
			storeExistenceBitFromAny(field.Addr().Interface())
		}
	}
}
