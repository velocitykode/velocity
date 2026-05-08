package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/orm/drivers"
)

// m2mMeta holds parsed metadata about a many-to-many relationship field.
type m2mMeta struct {
	fieldName    string       // Go struct field name (e.g. "Members")
	fieldIndex   int          // Index in the parent struct
	pivotTable   string       // Pivot table name (e.g. "team_members")
	localFK      string       // Pivot column referring to the parent (e.g. "team_id")
	relatedFK    string       // Pivot column referring to the related (e.g. "user_id")
	relatedType  reflect.Type // The related Go struct type
	relatedTable string       // Table name for the related type
	isPtr        bool         // Element is *T (slice of pointers)
}

// PivotResult pairs a related model with the extra columns of its pivot row.
type PivotResult[T any] struct {
	// Related is the loaded related model.
	Related T
	// Pivot maps non-FK columns of the pivot row to their values.
	Pivot map[string]any
}

// extractManyToManyValue returns the m2m tag value for an orm tag, or "".
// Format expected: "manyToMany:pivot_table,fk_local,fk_related"
func extractManyToManyValue(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "manyToMany:") {
			return strings.TrimPrefix(part, "manyToMany:")
		}
	}
	return ""
}

// parseManyToManyTag parses a manyToMany tag value of the form
// "pivot_table,fk_local,fk_related" and validates each identifier.
func parseManyToManyTag(value string) (pivot, localFK, relatedFK string, err error) {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("orm: invalid manyToMany tag %q - expected \"pivot_table,fk_local,fk_related\"", value)
	}
	pivot = strings.TrimSpace(parts[0])
	localFK = strings.TrimSpace(parts[1])
	relatedFK = strings.TrimSpace(parts[2])
	if pivot == "" || localFK == "" || relatedFK == "" {
		return "", "", "", fmt.Errorf("orm: manyToMany tag %q has empty parts", value)
	}
	if err := validateIdentifier(pivot); err != nil {
		return "", "", "", fmt.Errorf("orm: invalid pivot table in manyToMany tag: %w", err)
	}
	if err := validateIdentifier(localFK); err != nil {
		return "", "", "", fmt.Errorf("orm: invalid local FK in manyToMany tag: %w", err)
	}
	if err := validateIdentifier(relatedFK); err != nil {
		return "", "", "", fmt.Errorf("orm: invalid related FK in manyToMany tag: %w", err)
	}
	return pivot, localFK, relatedFK, nil
}

// findManyToManyField finds a struct field by name that has a manyToMany tag.
// Tries exact match first, then case-insensitive.
func findManyToManyField(modelType reflect.Type, name string) (reflect.StructField, int, bool) {
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if field.Name == name && extractManyToManyValue(field.Tag.Get("orm")) != "" {
			return field, i, true
		}
	}
	lowerName := strings.ToLower(name)
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if strings.ToLower(field.Name) == lowerName && extractManyToManyValue(field.Tag.Get("orm")) != "" {
			return field, i, true
		}
	}
	return reflect.StructField{}, 0, false
}

// resolveManyToManyMeta extracts m2m metadata for a named preload on a model type.
func resolveManyToManyMeta(modelType reflect.Type, preloadName string) (*m2mMeta, error) {
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	field, fieldIndex, found := findManyToManyField(modelType, preloadName)
	if !found {
		return nil, fmt.Errorf("orm: manyToMany relation %q not found on %s", preloadName, modelType.Name())
	}

	tagValue := extractManyToManyValue(field.Tag.Get("orm"))
	pivot, localFK, relatedFK, err := parseManyToManyTag(tagValue)
	if err != nil {
		return nil, err
	}

	fieldType := field.Type
	isPtr := false
	if fieldType.Kind() != reflect.Slice {
		return nil, fmt.Errorf("orm: manyToMany field %q must be a slice", field.Name)
	}
	elemType := fieldType.Elem()
	if elemType.Kind() == reflect.Ptr {
		isPtr = true
		elemType = elemType.Elem()
	}
	if elemType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("orm: manyToMany field %q must be a slice of structs", field.Name)
	}

	tableName := resolveTableNameReflect(elemType)
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("orm: invalid table name for related type %s: %w", elemType.Name(), err)
	}

	return &m2mMeta{
		fieldName:    field.Name,
		fieldIndex:   fieldIndex,
		pivotTable:   pivot,
		localFK:      localFK,
		relatedFK:    relatedFK,
		relatedType:  elemType,
		relatedTable: tableName,
		isPtr:        isPtr,
	}, nil
}

// loadM2M eager-loads a many-to-many relation onto each parent in models using
// at most two SQL queries: one against the pivot, one against the related table.
func (q *Query[T]) loadM2M(ctx context.Context, models *[]T, meta *m2mMeta) error {
	if len(*models) == 0 {
		return nil
	}

	// Collect parent ID values (always read from the parent's "id" column).
	seenParent := make(map[any]bool)
	parentIDs := make([]any, 0, len(*models))
	for i := range *models {
		v := reflect.ValueOf(&(*models)[i]).Elem()
		val, ok := getFieldValueByColumn(v, "id")
		if !ok {
			continue
		}
		normalized := normalizeKey(val)
		if isZeroKey(normalized) {
			continue
		}
		if !seenParent[normalized] {
			seenParent[normalized] = true
			parentIDs = append(parentIDs, val)
		}
	}
	if len(parentIDs) == 0 {
		return nil
	}

	// Discover pivot column names so we can group non-FK columns into Pivot maps.
	pivotCols, err := discoverPivotColumns(q.driver, ctx, meta.pivotTable)
	if err != nil {
		return fmt.Errorf("orm: failed to inspect pivot table %q: %w", meta.pivotTable, err)
	}
	pivotRows, relatedIDs, err := queryPivotRows(q.driver, ctx, meta, parentIDs, pivotCols)
	if err != nil {
		return err
	}
	if len(relatedIDs) == 0 {
		return nil
	}

	// Load related rows.
	related, err := queryRelatedRows(q.driver, ctx, meta, relatedIDs)
	if err != nil {
		return err
	}

	// Group related rows for fast lookup by related ID.
	relatedByID := make(map[any]reflect.Value, len(related))
	for _, rel := range related {
		idVal, ok := getFieldValueByColumn(rel, "id")
		if !ok {
			continue
		}
		relatedByID[normalizeKey(idVal)] = rel
	}

	// Group pivot rows by parent ID.
	type pivotRow struct {
		relatedID any
		pivotMap  map[string]any
	}
	byParent := make(map[any][]pivotRow, len(parentIDs))
	for _, pr := range pivotRows {
		byParent[normalizeKey(pr.parentID)] = append(byParent[normalizeKey(pr.parentID)], pivotRow{
			relatedID: pr.relatedID,
			pivotMap:  pr.pivotExtras,
		})
	}

	// Assign related slices back to each parent.
	for i := range *models {
		parentVal := reflect.ValueOf(&(*models)[i]).Elem()
		pidVal, ok := getFieldValueByColumn(parentVal, "id")
		if !ok {
			continue
		}
		rows := byParent[normalizeKey(pidVal)]
		if len(rows) == 0 {
			continue
		}

		field := parentVal.Field(meta.fieldIndex)
		slice := reflect.MakeSlice(field.Type(), 0, len(rows))
		for _, row := range rows {
			rel, ok := relatedByID[normalizeKey(row.relatedID)]
			if !ok {
				continue
			}
			if meta.isPtr {
				ptr := reflect.New(meta.relatedType)
				ptr.Elem().Set(rel)
				slice = reflect.Append(slice, ptr)
			} else {
				slice = reflect.Append(slice, rel)
			}
		}
		field.Set(slice)
	}

	return nil
}

// pivotRowResult is an internal carrier for parent->related linkage plus extras.
type pivotRowResult struct {
	parentID    any
	relatedID   any
	pivotExtras map[string]any
}

// queryPivotRows queries the pivot table for the given parent IDs and returns
// per-row linkage plus pivot extras (non-FK columns).
func queryPivotRows(driver drivers.Driver, ctx context.Context, meta *m2mMeta, parentIDs []any, pivotCols []string) ([]pivotRowResult, []any, error) {
	grammar := driver.Grammar()

	// SELECT column list: localFK, relatedFK, plus extras.
	var selectCols []string
	selectCols = append(selectCols, grammar.QuoteIdentifier(meta.localFK))
	selectCols = append(selectCols, grammar.QuoteIdentifier(meta.relatedFK))
	for _, c := range pivotCols {
		if c == meta.localFK || c == meta.relatedFK {
			continue
		}
		// Already validated by the driver-supplied schema lookup.
		if err := validateIdentifier(c); err != nil {
			continue
		}
		selectCols = append(selectCols, grammar.QuoteIdentifier(c))
	}

	placeholders := make([]string, len(parentIDs))
	for i := range parentIDs {
		placeholders[i] = grammar.Placeholder(i + 1)
	}

	pivotSQL := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
		strings.Join(selectCols, ", "),
		grammar.QuoteIdentifier(meta.pivotTable),
		grammar.QuoteIdentifier(meta.localFK),
		strings.Join(placeholders, ", "),
	)

	start := time.Now()
	rows, err := driver.QueryContext(ctx, pivotSQL, parentIDs...)
	if err != nil {
		dispatchQueryExecuted(ctx, pivotSQL, parentIDs, time.Since(start), 0, driver.DriverName(), 2)
		return nil, nil, fmt.Errorf("orm: failed to query pivot %q: %w", meta.pivotTable, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	var results []pivotRowResult
	relatedIDSeen := make(map[any]bool)
	var relatedIDs []any

	for rows.Next() {
		holders := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range holders {
			ptrs[i] = &holders[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("orm: failed to scan pivot row: %w", err)
		}

		extras := make(map[string]any)
		var parentID, relatedID any
		for i, c := range cols {
			val := holders[i]
			if b, ok := val.([]byte); ok {
				val = string(b)
			}
			switch c {
			case meta.localFK:
				parentID = val
			case meta.relatedFK:
				relatedID = val
			default:
				extras[c] = val
			}
		}
		results = append(results, pivotRowResult{
			parentID:    parentID,
			relatedID:   relatedID,
			pivotExtras: extras,
		})
		nk := normalizeKey(relatedID)
		if !isZeroKey(nk) && !relatedIDSeen[nk] {
			relatedIDSeen[nk] = true
			relatedIDs = append(relatedIDs, relatedID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	dispatchQueryExecuted(ctx, pivotSQL, parentIDs, time.Since(start), int64(len(results)), driver.DriverName(), 2)

	return results, relatedIDs, nil
}

// queryRelatedRows loads related rows by ID and returns them as []reflect.Value.
func queryRelatedRows(driver drivers.Driver, ctx context.Context, meta *m2mMeta, relatedIDs []any) ([]reflect.Value, error) {
	grammar := driver.Grammar()
	placeholders := make([]string, len(relatedIDs))
	for i := range relatedIDs {
		placeholders[i] = grammar.Placeholder(i + 1)
	}
	relSQL := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
		grammar.QuoteIdentifier(meta.relatedTable),
		grammar.QuoteIdentifier("id"),
		strings.Join(placeholders, ", "),
	)
	if checkSoftDelete(meta.relatedType) {
		relSQL += " AND " + grammar.QuoteIdentifier("deleted_at") + " IS NULL"
	}

	start := time.Now()
	rows, err := driver.QueryContext(ctx, relSQL, relatedIDs...)
	if err != nil {
		dispatchQueryExecuted(ctx, relSQL, relatedIDs, time.Since(start), 0, driver.DriverName(), 2)
		return nil, fmt.Errorf("orm: failed to load m2m related rows: %w", err)
	}
	defer rows.Close()

	var out []reflect.Value
	for rows.Next() {
		ptr := reflect.New(meta.relatedType)
		if err := scanIntoStruct(rows, ptr.Interface()); err != nil {
			return nil, fmt.Errorf("orm: failed to scan m2m related row: %w", err)
		}
		elem := ptr.Elem()
		markIsExisting(elem)
		out = append(out, elem)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dispatchQueryExecuted(ctx, relSQL, relatedIDs, time.Since(start), int64(len(out)), driver.DriverName(), 2)
	return out, nil
}

// pivotColumnCache memoizes column listings per (driver,table) combination.
var pivotColumnCache sync.Map // key: string ("driver|table") -> []string

// clearPivotColumnCache empties the pivot column cache without reassigning
// the variable. Tests use this between fresh manager setups so cached column
// lists from a prior in-memory database don't leak across runs. Reassigning
// the sync.Map variable would race with concurrent Load/Store calls in
// production code; Range/Delete operates on the existing instance safely.
func clearPivotColumnCache() {
	pivotColumnCache.Range(func(k, _ any) bool {
		pivotColumnCache.Delete(k)
		return true
	})
}

// discoverPivotColumns returns the column names of the pivot table. Results
// are cached per (driver, table) to avoid issuing schema queries on every call.
func discoverPivotColumns(driver drivers.Driver, ctx context.Context, table string) ([]string, error) {
	key := driver.DriverName() + "|" + table
	if cached, ok := pivotColumnCache.Load(key); ok {
		return cached.([]string), nil
	}
	grammar := driver.Grammar()
	// Fetch a single empty result set to read the column list. LIMIT 0 is
	// portable across all three supported drivers (sqlite, postgres, mysql).
	probeSQL := fmt.Sprintf("SELECT * FROM %s WHERE 1=0", grammar.QuoteIdentifier(table))
	rows, err := driver.QueryContext(ctx, probeSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	cp := make([]string, len(cols))
	copy(cp, cols)
	pivotColumnCache.Store(key, cp)
	return cp, nil
}

// LoadManyToManyWithPivot eagerly loads the named many-to-many relation on parent
// and returns the related rows paired with their pivot-row extra columns.
//
// Pivot extras are every pivot column except the two FK columns named in the
// manyToMany tag. Returns an empty slice (not an error) when the parent has
// no related rows. The parent ID is read from the parent's "id" column.
func LoadManyToManyWithPivot[T any, R any](parent *T, relationName string) ([]PivotResult[R], error) {
	if parent == nil {
		return nil, errors.New("orm: LoadManyToManyWithPivot: parent must not be nil")
	}
	mgr := Default()
	if mgr == nil {
		return nil, errors.New("orm: LoadManyToManyWithPivot: no default manager set")
	}
	driver := mgr.DefaultDriver()
	if driver == nil {
		return nil, errors.New("orm: LoadManyToManyWithPivot: no database connection")
	}

	parentType := reflect.TypeOf(*parent)
	meta, err := resolveManyToManyMeta(parentType, relationName)
	if err != nil {
		return nil, err
	}
	// Verify R matches the related type discovered from the tag, to fail
	// early on caller mismatches rather than panic in reflect later.
	if meta.relatedType != reflect.TypeOf(*new(R)) {
		return nil, fmt.Errorf("orm: LoadManyToManyWithPivot: type parameter %s does not match field %q (expected %s)",
			reflect.TypeOf(*new(R)).Name(), relationName, meta.relatedType.Name())
	}

	parentVal := reflect.ValueOf(parent).Elem()
	idVal, ok := getFieldValueByColumn(parentVal, "id")
	if !ok || isZeroKey(normalizeKey(idVal)) {
		return []PivotResult[R]{}, nil
	}

	ctx := context.Background()
	pivotCols, err := discoverPivotColumns(driver, ctx, meta.pivotTable)
	if err != nil {
		return nil, fmt.Errorf("orm: failed to inspect pivot table %q: %w", meta.pivotTable, err)
	}
	pivotRows, relatedIDs, err := queryPivotRows(driver, ctx, meta, []any{idVal}, pivotCols)
	if err != nil {
		return nil, err
	}
	if len(relatedIDs) == 0 {
		return []PivotResult[R]{}, nil
	}
	related, err := queryRelatedRows(driver, ctx, meta, relatedIDs)
	if err != nil {
		return nil, err
	}
	relatedByID := make(map[any]R, len(related))
	for _, rel := range related {
		idV, ok := getFieldValueByColumn(rel, "id")
		if !ok {
			continue
		}
		if r, ok := rel.Interface().(R); ok {
			relatedByID[normalizeKey(idV)] = r
		}
	}

	out := make([]PivotResult[R], 0, len(pivotRows))
	for _, pr := range pivotRows {
		rel, ok := relatedByID[normalizeKey(pr.relatedID)]
		if !ok {
			continue
		}
		out = append(out, PivotResult[R]{
			Related: rel,
			Pivot:   pr.pivotExtras,
		})
	}
	return out, nil
}

// M2MAccessor provides Attach/Detach/Sync helpers for a single parent's
// many-to-many relation. Construct one with M2M.
type M2MAccessor struct {
	driver       drivers.Driver
	pivotTable   string
	localFK      string
	relatedFK    string
	parentID     any
	relationName string
}

// M2M returns an accessor for the named many-to-many relation on parent.
// Use the accessor's Attach, Detach, and Sync methods to mutate pivot rows.
func M2M[T any](parent *T, relationName string) (*M2MAccessor, error) {
	if parent == nil {
		return nil, errors.New("orm: M2M: parent must not be nil")
	}
	mgr := Default()
	if mgr == nil {
		return nil, errors.New("orm: M2M: no default manager set")
	}
	driver := mgr.DefaultDriver()
	if driver == nil {
		return nil, errors.New("orm: M2M: no database connection")
	}
	parentType := reflect.TypeOf(*parent)
	meta, err := resolveManyToManyMeta(parentType, relationName)
	if err != nil {
		return nil, err
	}
	parentVal := reflect.ValueOf(parent).Elem()
	idVal, ok := getFieldValueByColumn(parentVal, "id")
	if !ok || isZeroKey(normalizeKey(idVal)) {
		return nil, errors.New("orm: M2M: parent has no id - save it first")
	}
	return &M2MAccessor{
		driver:       driver,
		pivotTable:   meta.pivotTable,
		localFK:      meta.localFK,
		relatedFK:    meta.relatedFK,
		parentID:     idVal,
		relationName: relationName,
	}, nil
}

// existingRelatedIDs returns the related-FK values currently linked to the parent.
func (a *M2MAccessor) existingRelatedIDs(ctx context.Context, q queryRunner) ([]any, error) {
	grammar := a.driver.Grammar()
	sqlStr := fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s",
		grammar.QuoteIdentifier(a.relatedFK),
		grammar.QuoteIdentifier(a.pivotTable),
		grammar.QuoteIdentifier(a.localFK),
		grammar.Placeholder(1),
	)
	rows, err := q.QueryContext(ctx, sqlStr, a.parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryRunner is the minimal contract shared by *sql.Tx and drivers.Driver
// used by accessor helpers so the same code path serves both transactional
// and direct calls.
type queryRunner interface {
	QueryContext(ctx context.Context, sqlStr string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, sqlStr string, args ...any) (sql.Result, error)
}

// Attach inserts pivot rows linking the parent to each id in ids that does
// not already have a pivot row. Existing links are preserved (no duplicate
// rows). Operates inside a single transaction.
func (a *M2MAccessor) Attach(ctx context.Context, ids ...any) error {
	if len(ids) == 0 {
		return nil
	}
	return a.runTx(ctx, func(tx *sql.Tx) error {
		existing, err := a.existingRelatedIDs(ctx, tx)
		if err != nil {
			return fmt.Errorf("orm: M2M.Attach: failed to read existing pivot rows: %w", err)
		}
		existingSet := make(map[any]bool, len(existing))
		for _, e := range existing {
			existingSet[normalizeKey(e)] = true
		}
		var toInsert []any
		seen := make(map[any]bool)
		for _, id := range ids {
			n := normalizeKey(id)
			if seen[n] || existingSet[n] {
				continue
			}
			seen[n] = true
			toInsert = append(toInsert, id)
		}
		if len(toInsert) == 0 {
			return nil
		}
		return a.insertPivotRows(ctx, tx, toInsert)
	})
}

// Detach removes pivot rows linking the parent to the supplied ids. With no
// ids, all pivot rows for the parent are removed. Operates inside a single
// transaction.
func (a *M2MAccessor) Detach(ctx context.Context, ids ...any) error {
	return a.runTx(ctx, func(tx *sql.Tx) error {
		return a.deleteRelated(ctx, tx, ids)
	})
}

// Sync makes the parent's pivot rows exactly match ids: missing rows are
// inserted, extra rows are deleted, and existing matches are left alone.
// All changes happen in a single transaction.
func (a *M2MAccessor) Sync(ctx context.Context, ids ...any) error {
	return a.runTx(ctx, func(tx *sql.Tx) error {
		existing, err := a.existingRelatedIDs(ctx, tx)
		if err != nil {
			return fmt.Errorf("orm: M2M.Sync: failed to read existing pivot rows: %w", err)
		}
		existingSet := make(map[any]bool, len(existing))
		for _, e := range existing {
			existingSet[normalizeKey(e)] = true
		}
		desiredSet := make(map[any]bool, len(ids))
		desiredOrder := make([]any, 0, len(ids))
		for _, id := range ids {
			n := normalizeKey(id)
			if !desiredSet[n] {
				desiredSet[n] = true
				desiredOrder = append(desiredOrder, id)
			}
		}

		var toRemove []any
		for _, e := range existing {
			if !desiredSet[normalizeKey(e)] {
				toRemove = append(toRemove, e)
			}
		}
		var toAdd []any
		for _, id := range desiredOrder {
			if !existingSet[normalizeKey(id)] {
				toAdd = append(toAdd, id)
			}
		}

		if len(toRemove) > 0 {
			if err := a.deleteRelated(ctx, tx, toRemove); err != nil {
				return err
			}
		}
		if len(toAdd) > 0 {
			if err := a.insertPivotRows(ctx, tx, toAdd); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *M2MAccessor) insertPivotRows(ctx context.Context, tx queryRunner, ids []any) error {
	grammar := a.driver.Grammar()
	for _, id := range ids {
		// One row per insert keeps SQL simple and portable across drivers
		// without needing to special-case multi-row INSERT placeholders.
		sqlStr := fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (%s, %s)",
			grammar.QuoteIdentifier(a.pivotTable),
			grammar.QuoteIdentifier(a.localFK),
			grammar.QuoteIdentifier(a.relatedFK),
			grammar.Placeholder(1),
			grammar.Placeholder(2),
		)
		if _, err := tx.ExecContext(ctx, sqlStr, a.parentID, id); err != nil {
			return fmt.Errorf("orm: M2M: insert pivot row failed: %w", err)
		}
	}
	return nil
}

func (a *M2MAccessor) deleteRelated(ctx context.Context, tx queryRunner, ids []any) error {
	grammar := a.driver.Grammar()
	if len(ids) == 0 {
		// Detach-all: drop every pivot row for this parent.
		sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s = %s",
			grammar.QuoteIdentifier(a.pivotTable),
			grammar.QuoteIdentifier(a.localFK),
			grammar.Placeholder(1),
		)
		if _, err := tx.ExecContext(ctx, sqlStr, a.parentID); err != nil {
			return fmt.Errorf("orm: M2M: detach-all failed: %w", err)
		}
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, a.parentID)
	for i, id := range ids {
		placeholders[i] = grammar.Placeholder(i + 2)
		args = append(args, id)
	}
	sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s = %s AND %s IN (%s)",
		grammar.QuoteIdentifier(a.pivotTable),
		grammar.QuoteIdentifier(a.localFK),
		grammar.Placeholder(1),
		grammar.QuoteIdentifier(a.relatedFK),
		strings.Join(placeholders, ", "),
	)
	if _, err := tx.ExecContext(ctx, sqlStr, args...); err != nil {
		return fmt.Errorf("orm: M2M: detach failed: %w", err)
	}
	return nil
}

func (a *M2MAccessor) runTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := a.driver.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("orm: M2M: begin tx failed: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
