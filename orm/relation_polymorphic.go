package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/velocitykode/velocity/orm/drivers"
)

// polymorphicMeta holds parsed metadata about a polymorphic relation field.
type polymorphicMeta struct {
	fieldName  string // Go struct field name (e.g. "Resource")
	fieldIndex int    // Index in the parent struct
	typeColumn string // Column name carrying the type-name discriminator
	idColumn   string // Column name carrying the foreign-key id
}

// extractPolymorphicValue returns the polymorphic tag value for an orm tag, or "".
// Format expected: "polymorphic:type_col,id_col"
func extractPolymorphicValue(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "polymorphic:") {
			return strings.TrimPrefix(part, "polymorphic:")
		}
	}
	return ""
}

// parsePolymorphicTag parses a polymorphic tag value of the form
// "type_col,id_col" and validates each identifier.
func parsePolymorphicTag(value string) (typeCol, idCol string, err error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("orm: invalid polymorphic tag %q - expected \"type_col,id_col\"", value)
	}
	typeCol = strings.TrimSpace(parts[0])
	idCol = strings.TrimSpace(parts[1])
	if typeCol == "" || idCol == "" {
		return "", "", fmt.Errorf("orm: polymorphic tag %q has empty parts", value)
	}
	if err := validateIdentifier(typeCol); err != nil {
		return "", "", fmt.Errorf("orm: invalid type column in polymorphic tag: %w", err)
	}
	if err := validateIdentifier(idCol); err != nil {
		return "", "", fmt.Errorf("orm: invalid id column in polymorphic tag: %w", err)
	}
	return typeCol, idCol, nil
}

// findPolymorphicField finds a struct field by name that has a polymorphic tag.
func findPolymorphicField(modelType reflect.Type, name string) (reflect.StructField, int, bool) {
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if field.Name == name && extractPolymorphicValue(field.Tag.Get("orm")) != "" {
			return field, i, true
		}
	}
	lowerName := strings.ToLower(name)
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if strings.ToLower(field.Name) == lowerName && extractPolymorphicValue(field.Tag.Get("orm")) != "" {
			return field, i, true
		}
	}
	return reflect.StructField{}, 0, false
}

// resolvePolymorphicMeta extracts polymorphic metadata for a named preload.
func resolvePolymorphicMeta(modelType reflect.Type, preloadName string) (*polymorphicMeta, error) {
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	field, idx, found := findPolymorphicField(modelType, preloadName)
	if !found {
		return nil, fmt.Errorf("orm: polymorphic relation %q not found on %s", preloadName, modelType.Name())
	}
	tagVal := extractPolymorphicValue(field.Tag.Get("orm"))
	typeCol, idCol, err := parsePolymorphicTag(tagVal)
	if err != nil {
		return nil, err
	}
	if field.Type != reflect.TypeOf(Morph{}) {
		return nil, fmt.Errorf("orm: polymorphic field %q must be of type orm.Morph", field.Name)
	}
	return &polymorphicMeta{
		fieldName:  field.Name,
		fieldIndex: idx,
		typeColumn: typeCol,
		idColumn:   idCol,
	}, nil
}

// loadPolymorphic eagerly loads a polymorphic relation onto each parent in
// models. Issues at most K queries, where K is the number of distinct type
// names present across the parents (one IN query per type).
func (q *Query[T]) loadPolymorphic(ctx context.Context, models *[]T, meta *polymorphicMeta) error {
	if len(*models) == 0 {
		return nil
	}

	// 1. Group parent IDs by morph type.
	type idIdx struct {
		id       any
		modelIdx int
	}
	byType := make(map[string][]idIdx)
	for i := range *models {
		v := reflect.ValueOf(&(*models)[i]).Elem()
		field := v.Field(meta.fieldIndex)
		if field.Kind() != reflect.Struct {
			continue
		}
		tName := field.FieldByName("TypeName").String()
		idV := field.FieldByName("ID").Interface()
		if tName == "" || idV == nil || isZeroKey(normalizeKey(idV)) {
			continue
		}
		byType[tName] = append(byType[tName], idIdx{id: idV, modelIdx: i})
	}
	if len(byType) == 0 {
		return nil
	}

	// 2. For each type, look up the registered model type, batch-load by IN.
	for tName, items := range byType {
		relatedType, ok := LookupMorph(tName)
		if !ok {
			// Strict mode: hard-fail the whole batch (preserves the
			// original error path for callers that opt in).
			if MorphStrict() {
				return fmt.Errorf("orm: polymorphic relation %q: unknown morph type %q - call orm.RegisterMorph(%q, reflect.TypeOf(YourModel{})) at startup", meta.fieldName, tName, tName)
			}
			// Non-strict (default): log a warning and skip rows of this
			// type so a single drifted row cannot crash a list view.
			// Affected rows keep Resolved=nil and the caller can detect
			// the unresolved morph via Morph.IsZero/TypeName checks.
			if w := morphWarnWriter(); w != nil {
				fmt.Fprintf(w, "orm: polymorphic relation %q: unknown morph type %q - skipping %d row(s); call orm.RegisterMorph(%q, reflect.TypeOf(YourModel{})) at startup or SetMorphStrict(true) to fail fast\n", meta.fieldName, tName, len(items), tName)
			}
			continue
		}
		// Deduplicate IDs while keeping a mapping from id -> []modelIdx.
		idSeen := make(map[any]bool, len(items))
		var uniqueIDs []any
		idToModelIdxs := make(map[any][]int, len(items))
		for _, it := range items {
			n := normalizeKey(it.id)
			if !idSeen[n] {
				idSeen[n] = true
				uniqueIDs = append(uniqueIDs, it.id)
			}
			idToModelIdxs[n] = append(idToModelIdxs[n], it.modelIdx)
		}

		loaded, err := loadByIDs(q.driver, ctx, relatedType, uniqueIDs)
		if err != nil {
			return fmt.Errorf("orm: failed to eager-load polymorphic %q (%s): %w", meta.fieldName, tName, err)
		}

		// Index loaded rows by id for assignment.
		loadedByID := make(map[any]any, len(loaded))
		for _, row := range loaded {
			rv := reflect.ValueOf(row).Elem()
			idV, ok := getFieldValueByColumn(rv, "id")
			if !ok {
				continue
			}
			loadedByID[normalizeKey(idV)] = row
		}

		for nKey, idxs := range idToModelIdxs {
			row, ok := loadedByID[nKey]
			if !ok {
				continue
			}
			for _, mi := range idxs {
				v := reflect.ValueOf(&(*models)[mi]).Elem()
				morphField := v.Field(meta.fieldIndex)
				resolvedField := morphField.FieldByName("Resolved")
				if resolvedField.IsValid() && resolvedField.CanSet() {
					resolvedField.Set(reflect.ValueOf(row))
				}
			}
		}
	}
	return nil
}

// loadByIDs loads rows from the table for relatedType where id IN (ids).
// Returns a slice of pointer-to-relatedType values, with IsExisting set.
//
// Scope semantics: the SELECT honours every global scope registered for
// relatedType. Builds the IN predicate plus scope conditions through
// buildScopedInSelect so a polymorphic eager-load cannot leak rows
// from outside the caller's tenant / archive / locale / state scope.
func loadByIDs(driver drivers.Driver, ctx context.Context, relatedType reflect.Type, ids []any) ([]any, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tableName := resolveTableNameReflect(relatedType)
	if err := validateIdentifier(tableName); err != nil {
		return nil, fmt.Errorf("orm: invalid table name for %s: %w", relatedType.Name(), err)
	}
	sqlStr, sqlArgs, scopeErr := buildScopedInSelect(ctx, driver, relatedType, tableName, "id", ids)
	if scopeErr != nil {
		return nil, fmt.Errorf("orm: failed to apply scopes for polymorphic %s: %w", relatedType.Name(), scopeErr)
	}
	rows, err := driver.QueryContext(ctx, sqlStr, sqlArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// The scan plan is resolved once for the whole result set (lazily,
	// so an empty result set never touches rows.Columns).
	var out []any
	var plan *scanPlan
	for rows.Next() {
		if plan == nil {
			var perr error
			if plan, perr = newScanPlan(rows, relatedType); perr != nil {
				return nil, perr
			}
		}
		ptr := reflect.New(relatedType)
		if err := plan.scanRow(rows, ptr.Elem()); err != nil {
			return nil, err
		}
		markIsExisting(ptr.Elem())
		out = append(out, ptr.Interface())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
