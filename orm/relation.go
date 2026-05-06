package orm

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

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

// resolveTableNameReflect resolves the table name for a type using reflection.
// Checks for TableName() method, falls back to lowercase+s convention.
func resolveTableNameReflect(t reflect.Type) string {
	// Try value receiver first (most common for TableName)
	if method, ok := t.MethodByName("TableName"); ok {
		if method.Type.NumIn() == 1 && method.Type.NumOut() == 1 && method.Type.Out(0).Kind() == reflect.String {
			receiver := reflect.New(t).Elem()
			result := method.Func.Call([]reflect.Value{receiver})
			if name, ok := result[0].Interface().(string); ok && name != "" {
				return name
			}
		}
	}

	// Try pointer receiver
	ptrType := reflect.PointerTo(t)
	if method, ok := ptrType.MethodByName("TableName"); ok {
		if method.Type.NumIn() == 1 && method.Type.NumOut() == 1 && method.Type.Out(0).Kind() == reflect.String {
			receiver := reflect.New(t)
			result := method.Func.Call([]reflect.Value{receiver})
			if name, ok := result[0].Interface().(string); ok && name != "" {
				return name
			}
		}
	}

	// Default: lowercase + "s"
	name := t.Name()
	if name == "" {
		return ""
	}
	return strings.ToLower(name) + "s"
}

// getFieldValueByColumn extracts the value of a struct field matching the given column name.
// Walks into embedded structs to access base model fields (ID, CreatedAt, etc.).
func getFieldValueByColumn(v reflect.Value, columnName string) (any, bool) {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		if !field.IsExported() {
			continue
		}

		// Recurse into embedded (anonymous) structs, except time.Time
		if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Type != reflect.TypeOf(time.Time{}) {
			if val, ok := getFieldValueByColumn(v.Field(i), columnName); ok {
				return val, true
			}
			continue
		}

		col := resolveColumnName(field)
		if col == columnName {
			return v.Field(i).Interface(), true
		}
	}
	return nil, false
}

// resolveColumnName determines the database column name for a struct field.
func resolveColumnName(field reflect.StructField) string {
	tag := field.Tag.Get("orm")
	if tag == "-" {
		return ""
	}
	if tag != "" {
		parts := strings.Split(tag, ";")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "column:") {
				return strings.TrimPrefix(part, "column:")
			}
			if part == "primaryKey" || strings.HasPrefix(part, "primaryKey;") {
				return "id"
			}
		}
	}
	return toSnakeCase(field.Name)
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

// markIsExisting sets the IsExisting flag on a model's embedded base type.
func markIsExisting(v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			existsField := v.Field(i).FieldByName("IsExisting")
			if existsField.IsValid() && existsField.CanSet() && existsField.Kind() == reflect.Bool {
				existsField.SetBool(true)
				return
			}
		}
	}
}

// loadRelations implements eager loading for preloaded relationships.
// Called by Query[T].Get() after the primary query results are loaded.
//
// Dispatches by tag prefix on the named field:
//   - relation:    -> hasOne/hasMany/belongsTo via loadRelation
//   - manyToMany:  -> many-to-many pivot via loadM2M
//   - polymorphic: -> morph batched per type via loadPolymorphic
func (q *Query[T]) loadRelations(models *[]T) error {
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
				if err := q.loadM2M(models, meta); err != nil {
					return err
				}
				continue
			case extractPolymorphicValue(tag) != "":
				meta, err := resolvePolymorphicMeta(modelType, preload)
				if err != nil {
					return err
				}
				if err := q.loadPolymorphic(models, meta); err != nil {
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
		if err := q.loadRelation(models, meta); err != nil {
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
func (q *Query[T]) loadRelation(models *[]T, meta *relationMeta) error {
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

	// 2. Build and execute: SELECT * FROM related_table WHERE queryColumn IN (?, ?, ...)
	grammar := q.driver.Grammar()

	placeholders := make([]string, len(keys))
	for i := range keys {
		placeholders[i] = grammar.Placeholder(i + 1)
	}

	relSQL := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)",
		grammar.QuoteIdentifier(meta.relatedTable),
		grammar.QuoteIdentifier(queryColumn),
		strings.Join(placeholders, ", "),
	)

	// Respect soft deletes on the related model
	if checkSoftDelete(meta.relatedType) {
		relSQL += " AND " + grammar.QuoteIdentifier("deleted_at") + " IS NULL"
	}

	start := time.Now()
	rows, err := q.driver.QueryContext(q.getContext(), relSQL, keys...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(q.getContext(), relSQL, keys, duration, 0, q.driver.DriverName(), 2)
		return fmt.Errorf("orm: failed to load relation %q: %w", meta.fieldName, err)
	}
	defer rows.Close()

	// 3. Scan results and group by the query column value
	groups := make(map[any][]reflect.Value)
	var rowCount int64
	for rows.Next() {
		ptr := reflect.New(meta.relatedType)
		if err := scanIntoStruct(rows, ptr.Interface()); err != nil {
			return fmt.Errorf("orm: failed to scan relation %q: %w", meta.fieldName, err)
		}
		elem := ptr.Elem()
		markIsExisting(elem)
		rowCount++

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

	dispatchQueryExecuted(q.getContext(), relSQL, keys, duration, rowCount, q.driver.DriverName(), 2)

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
}

// assignSingle sets a single-value field (HasOne/BelongsTo) from the first match.
func assignSingle(field reflect.Value, matches []reflect.Value, meta *relationMeta) {
	if len(matches) == 0 {
		return
	}
	if meta.isPtr {
		ptr := reflect.New(meta.relatedType)
		ptr.Elem().Set(matches[0])
		field.Set(ptr)
	} else {
		field.Set(matches[0])
	}
}
