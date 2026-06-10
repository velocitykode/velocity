package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// --- Test model types ---

type RelUser struct {
	Model[RelUser]
	Name    string      `orm:"column:name" json:"name"`
	Profile *RelProfile `orm:"relation:hasOne,user_id,id" json:"profile,omitempty"`
	Posts   []RelPost   `orm:"relation:hasMany,user_id,id" json:"posts,omitempty"`
}

func (RelUser) TableName() string { return "rel_users" }

type RelPost struct {
	Model[RelPost]
	UserID uint     `orm:"column:user_id" json:"user_id"`
	Title  string   `orm:"column:title" json:"title"`
	User   *RelUser `orm:"relation:belongsTo,user_id,id" json:"user,omitempty"`
}

func (RelPost) TableName() string { return "rel_posts" }

type RelProfile struct {
	Model[RelProfile]
	UserID uint     `orm:"column:user_id" json:"user_id"`
	Bio    string   `orm:"column:bio" json:"bio"`
	User   *RelUser `orm:"relation:belongsTo,user_id,id" json:"user,omitempty"`
}

func (RelProfile) TableName() string { return "rel_profiles" }

// Model with pointer-to-slice relation field ([]*T instead of []T)
type RelUserPtrSlice struct {
	Model[RelUserPtrSlice]
	Name  string        `orm:"column:name" json:"name"`
	Posts []*RelPtrPost `orm:"relation:hasMany,user_id,id" json:"posts,omitempty"`
}

func (RelUserPtrSlice) TableName() string { return "rel_users" }

type RelPtrPost struct {
	Model[RelPtrPost]
	UserID uint   `orm:"column:user_id" json:"user_id"`
	Title  string `orm:"column:title" json:"title"`
}

func (RelPtrPost) TableName() string { return "rel_posts" }

// Model without a TableName() method — tests convention-based naming
type NoTableNameModel struct {
	Model[NoTableNameModel]
	Value string `orm:"column:value"`
}

// Model with soft deletes for relationship testing
type SoftUser struct {
	SoftDeleteModel[SoftUser]
	Name  string     `orm:"column:name" json:"name"`
	Posts []SoftPost `orm:"relation:hasMany,user_id,id" json:"posts,omitempty"`
}

func (SoftUser) TableName() string { return "soft_users" }

type SoftPost struct {
	SoftDeleteModel[SoftPost]
	UserID uint   `orm:"column:user_id" json:"user_id"`
	Title  string `orm:"column:title" json:"title"`
}

func (SoftPost) TableName() string { return "soft_posts" }

// Model with a non-struct relation field (invalid)
type BadRelationModel struct {
	Model[BadRelationModel]
	Tags string `orm:"relation:hasMany,parent_id,id"`
}

func (BadRelationModel) TableName() string { return "bad_models" }

// Model with an unsafe identifier in its relation tag
type UnsafeTagModel struct {
	Model[UnsafeTagModel]
	Items []RelPost `orm:"relation:hasMany,user_id; DROP TABLE users--,id"`
}

func (UnsafeTagModel) TableName() string { return "unsafe_models" }

// --- Table setup ---

func setupRelationTables(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()

	for _, ddl := range []string{
		`CREATE TABLE rel_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE rel_posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE rel_profiles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			bio TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return manager
}

func seedRelationData(t *testing.T, manager *Manager) {
	t.Helper()
	db := manager.DB()

	seeds := []string{
		// Users: Alice, Bob, Charlie
		`INSERT INTO rel_users (id, name, created_at, updated_at) VALUES
			(1, 'Alice', '2024-01-01', '2024-01-01'),
			(2, 'Bob', '2024-01-01', '2024-01-01'),
			(3, 'Charlie', '2024-01-01', '2024-01-01')`,
		// Posts: Alice has 2, Bob has 1, Charlie has 0
		`INSERT INTO rel_posts (id, user_id, title, created_at, updated_at) VALUES
			(1, 1, 'Alice Post 1', '2024-01-01', '2024-01-01'),
			(2, 1, 'Alice Post 2', '2024-01-01', '2024-01-01'),
			(3, 2, 'Bob Post 1', '2024-01-01', '2024-01-01')`,
		// Profiles: Alice and Bob have profiles, Charlie does not
		`INSERT INTO rel_profiles (id, user_id, bio, created_at, updated_at) VALUES
			(1, 1, 'Alice bio', '2024-01-01', '2024-01-01'),
			(2, 2, 'Bob bio', '2024-01-01', '2024-01-01')`,
	}
	for _, sql := range seeds {
		if _, err := db.Exec(sql); err != nil {
			t.Fatalf("failed to seed: %v", err)
		}
	}
}

func withRelationDB(t *testing.T) func() {
	t.Helper()
	manager := setupRelationTables(t)
	seedRelationData(t, manager)
	SetDefault(manager)
	return func() {
		ResetDefault()
		manager.Shutdown(context.Background())
	}
}

// ============================================================
// Unit tests: parseRelationTag
// ============================================================

func TestParseRelationTag(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		relType RelationType
		fk      string
		lk      string
		wantErr bool
	}{
		{name: "HasOne", tag: "hasOne,user_id,id", relType: HasOne, fk: "user_id", lk: "id"},
		{name: "HasMany", tag: "hasMany,user_id,id", relType: HasMany, fk: "user_id", lk: "id"},
		{name: "BelongsTo", tag: "belongsTo,user_id,id", relType: BelongsTo, fk: "user_id", lk: "id"},
		{name: "WhitespaceAround", tag: "hasMany, user_id , id", relType: HasMany, fk: "user_id", lk: "id"},
		{name: "ErrorUnknownType", tag: "manyToMany,user_id,id", wantErr: true},
		{name: "ErrorTooFewParts", tag: "hasMany,user_id", wantErr: true},
		{name: "ErrorTooManyParts", tag: "hasMany,a,b,c", wantErr: true},
		{name: "ErrorEmptyForeignKey", tag: "hasMany,,id", wantErr: true},
		{name: "ErrorEmptyLocalKey", tag: "hasMany,user_id,", wantErr: true},
		{name: "ErrorEmptyString", tag: "", wantErr: true},
		{name: "ErrorSingleValue", tag: "hasMany", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relType, fk, lk, err := parseRelationTag(tt.tag)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if relType != tt.relType {
				t.Errorf("relType = %d, want %d", relType, tt.relType)
			}
			if fk != tt.fk {
				t.Errorf("fk = %q, want %q", fk, tt.fk)
			}
			if lk != tt.lk {
				t.Errorf("lk = %q, want %q", lk, tt.lk)
			}
		})
	}
}

// ============================================================
// Unit tests: extractRelationValue
// ============================================================

func TestExtractRelationValue(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want string
	}{
		{name: "RelationOnly", tag: "relation:hasMany,user_id,id", want: "hasMany,user_id,id"},
		{name: "RelationAfterColumn", tag: "column:name;relation:hasOne,user_id,id", want: "hasOne,user_id,id"},
		{name: "RelationBeforeColumn", tag: "relation:belongsTo,fk,pk;column:name", want: "belongsTo,fk,pk"},
		{name: "NoRelation", tag: "column:name", want: ""},
		{name: "EmptyTag", tag: "", want: ""},
		{name: "DashTag", tag: "-", want: ""},
		{name: "WhitespaceAround", tag: " relation:hasMany,fk,pk ", want: "hasMany,fk,pk"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRelationValue(tt.tag)
			if got != tt.want {
				t.Errorf("extractRelationValue(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

// ============================================================
// Unit tests: resolveTableNameReflect
// ============================================================

func TestResolveTableNameReflect(t *testing.T) {
	tests := []struct {
		name     string
		typ      reflect.Type
		wantName string
	}{
		{name: "WithTableNameMethod", typ: reflect.TypeOf(RelUser{}), wantName: "rel_users"},
		{name: "WithTableNameMethod_Post", typ: reflect.TypeOf(RelPost{}), wantName: "rel_posts"},
		// B7 unification: the fallback is now str.Plural(ToSnakeCase(name)),
		// so a multi-word type name pluralizes to snake_case. Previously this
		// path produced the lowercase-concatenated "notablenamemodels"; the
		// write path already produced "no_table_name_models", and the two now
		// agree. Override TableName() to pin a legacy name.
		{name: "WithoutTableNameMethod", typ: reflect.TypeOf(NoTableNameModel{}), wantName: "no_table_name_models"},
		{name: "AnonymousStruct", typ: reflect.TypeOf(struct{ X int }{}), wantName: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTableNameReflect(tt.typ)
			if got != tt.wantName {
				t.Errorf("resolveTableNameReflect(%s) = %q, want %q", tt.typ.Name(), got, tt.wantName)
			}
		})
	}
}

// ============================================================
// Unit tests: resolveColumnName
// ============================================================

func TestResolveColumnName(t *testing.T) {
	type sample struct {
		PlainField   string
		WithColumn   string `orm:"column:custom_col"`
		WithPK       uint   `orm:"primaryKey;autoIncrement"`
		Skipped      string `orm:"-"`
		CamelCase    string
		ProviderID   uint
		RelationSkip string `orm:"relation:hasMany,fk,pk"`
	}

	tests := []struct {
		name    string
		field   string
		wantCol string
	}{
		{name: "PlainFieldSnakeCase", field: "PlainField", wantCol: "plain_field"},
		{name: "ExplicitColumn", field: "WithColumn", wantCol: "custom_col"},
		{name: "PrimaryKeyMapsToID", field: "WithPK", wantCol: "id"},
		{name: "DashReturnsEmpty", field: "Skipped", wantCol: ""},
		{name: "CamelToSnake", field: "CamelCase", wantCol: "camel_case"},
		{name: "ConsecutiveCaps", field: "ProviderID", wantCol: "provider_id"},
	}

	typ := reflect.TypeOf(sample{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, ok := typ.FieldByName(tt.field)
			if !ok {
				t.Fatalf("field %q not found", tt.field)
			}
			got := resolveColumnName(field)
			if got != tt.wantCol {
				t.Errorf("resolveColumnName(%q) = %q, want %q", tt.field, got, tt.wantCol)
			}
		})
	}
}

// ============================================================
// Unit tests: normalizeKey
// ============================================================

func TestNormalizeKey(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{name: "Uint", input: uint(42), want: int64(42)},
		{name: "Uint8", input: uint8(42), want: int64(42)},
		{name: "Uint16", input: uint16(42), want: int64(42)},
		{name: "Uint32", input: uint32(42), want: int64(42)},
		{name: "Uint64", input: uint64(42), want: int64(42)},
		{name: "Int", input: int(42), want: int64(42)},
		{name: "Int8", input: int8(42), want: int64(42)},
		{name: "Int16", input: int16(42), want: int64(42)},
		{name: "Int32", input: int32(42), want: int64(42)},
		{name: "Int64Passthrough", input: int64(42), want: int64(42)},
		{name: "StringPassthrough", input: "uuid-abc", want: "uuid-abc"},
		{name: "BoolPassthrough", input: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeKey(tt.input)
			if got != tt.want {
				t.Errorf("normalizeKey(%v [%T]) = %v [%T], want %v [%T]",
					tt.input, tt.input, got, got, tt.want, tt.want)
			}
		})
	}
}

func TestNormalizeKey_CrossTypeEquality(t *testing.T) {
	// The whole point of normalizeKey: uint(5) and int(5) must produce the same map key
	m := make(map[any]string)
	m[normalizeKey(uint(5))] = "from_uint"

	if m[normalizeKey(int(5))] != "from_uint" {
		t.Error("normalizeKey(uint(5)) and normalizeKey(int(5)) must be equal as map keys")
	}
	if m[normalizeKey(int64(5))] != "from_uint" {
		t.Error("normalizeKey(uint(5)) and normalizeKey(int64(5)) must be equal as map keys")
	}
}

// ============================================================
// Unit tests: isZeroKey
// ============================================================

func TestIsZeroKey(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  bool
	}{
		{name: "Nil", input: nil, want: true},
		{name: "Int64Zero", input: int64(0), want: true},
		{name: "EmptyString", input: "", want: true},
		{name: "Int64Nonzero", input: int64(1), want: false},
		{name: "Int64Negative", input: int64(-1), want: false},
		{name: "NonEmptyString", input: "abc", want: false},
		{name: "UnknownTypeNotZero", input: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isZeroKey(tt.input)
			if got != tt.want {
				t.Errorf("isZeroKey(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ============================================================
// Unit tests: getFieldValueByColumn
// ============================================================

func TestGetFieldValueByColumn(t *testing.T) {
	user := RelUser{Name: "Alice"}
	user.Model.ID = 42

	tests := []struct {
		name    string
		column  string
		wantVal any
		wantOK  bool
	}{
		{name: "EmbeddedID", column: "id", wantVal: uint(42), wantOK: true},
		{name: "DirectField", column: "name", wantVal: "Alice", wantOK: true},
		{name: "NonexistentColumn", column: "nonexistent", wantOK: false},
		{name: "EmbeddedTimestamp", column: "created_at", wantOK: true}, // exists but zero
	}

	v := reflect.ValueOf(user)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, ok := getFieldValueByColumn(v, tt.column)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantVal != nil && val != tt.wantVal {
				t.Errorf("value = %v, want %v", val, tt.wantVal)
			}
		})
	}
}

func TestGetFieldValueByColumn_NilPointer(t *testing.T) {
	var user *RelUser // nil pointer
	v := reflect.ValueOf(user)
	_, ok := getFieldValueByColumn(v, "id")
	if ok {
		t.Error("expected false for nil pointer receiver")
	}
}

func TestGetFieldValueByColumn_ForeignKey(t *testing.T) {
	// Verify we can extract FK values from related models (used in grouping)
	post := RelPost{UserID: 7, Title: "test"}
	v := reflect.ValueOf(post)
	val, ok := getFieldValueByColumn(v, "user_id")
	if !ok {
		t.Fatal("expected to find user_id column on RelPost")
	}
	if val.(uint) != 7 {
		t.Errorf("user_id = %v, want 7", val)
	}
}

// ============================================================
// Unit tests: findRelationField
// ============================================================

func TestFindRelationField(t *testing.T) {
	modelType := reflect.TypeOf(RelUser{})

	tests := []struct {
		name      string
		preload   string
		wantName  string
		wantFound bool
	}{
		{name: "ExactMatch", preload: "Posts", wantName: "Posts", wantFound: true},
		{name: "ExactMatchSingle", preload: "Profile", wantName: "Profile", wantFound: true},
		{name: "CaseInsensitiveLower", preload: "posts", wantName: "Posts", wantFound: true},
		{name: "CaseInsensitiveMixed", preload: "PROFILE", wantName: "Profile", wantFound: true},
		{name: "NonExistentField", preload: "Comments", wantFound: false},
		{name: "ExistsButNoRelationTag", preload: "Name", wantFound: false},
		{name: "EmbeddedModelField", preload: "Model", wantFound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, _, found := findRelationField(modelType, tt.preload)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if found && field.Name != tt.wantName {
				t.Errorf("field.Name = %q, want %q", field.Name, tt.wantName)
			}
		})
	}
}

// ============================================================
// Unit tests: resolveRelationMeta
// ============================================================

func TestResolveRelationMeta_HasMany(t *testing.T) {
	meta, err := resolveRelationMeta(reflect.TypeOf(RelUser{}), "Posts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.relType != HasMany {
		t.Errorf("relType = %d, want HasMany (%d)", meta.relType, HasMany)
	}
	if meta.foreignKey != "user_id" {
		t.Errorf("foreignKey = %q, want %q", meta.foreignKey, "user_id")
	}
	if meta.localKey != "id" {
		t.Errorf("localKey = %q, want %q", meta.localKey, "id")
	}
	if meta.relatedTable != "rel_posts" {
		t.Errorf("relatedTable = %q, want %q", meta.relatedTable, "rel_posts")
	}
	if !meta.isSlice {
		t.Error("expected isSlice=true for []RelPost")
	}
	if meta.isPtr {
		t.Error("expected isPtr=false for []RelPost (value elements)")
	}
}

func TestResolveRelationMeta_HasOne(t *testing.T) {
	meta, err := resolveRelationMeta(reflect.TypeOf(RelUser{}), "Profile")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.relType != HasOne {
		t.Errorf("relType = %d, want HasOne (%d)", meta.relType, HasOne)
	}
	if meta.isSlice {
		t.Error("expected isSlice=false for *RelProfile")
	}
	if !meta.isPtr {
		t.Error("expected isPtr=true for *RelProfile")
	}
}

func TestResolveRelationMeta_BelongsTo(t *testing.T) {
	meta, err := resolveRelationMeta(reflect.TypeOf(RelPost{}), "User")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.relType != BelongsTo {
		t.Errorf("relType = %d, want BelongsTo (%d)", meta.relType, BelongsTo)
	}
	if meta.foreignKey != "user_id" {
		t.Errorf("foreignKey = %q, want %q", meta.foreignKey, "user_id")
	}
	if meta.localKey != "id" {
		t.Errorf("localKey = %q, want %q", meta.localKey, "id")
	}
	if meta.relatedTable != "rel_users" {
		t.Errorf("relatedTable = %q, want %q", meta.relatedTable, "rel_users")
	}
}

func TestResolveRelationMeta_PointerSlice(t *testing.T) {
	meta, err := resolveRelationMeta(reflect.TypeOf(RelUserPtrSlice{}), "Posts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !meta.isSlice {
		t.Error("expected isSlice=true for []*RelPtrPost")
	}
	if !meta.isPtr {
		t.Error("expected isPtr=true for []*RelPtrPost (pointer elements)")
	}
}

func TestResolveRelationMeta_ErrorNonExistent(t *testing.T) {
	_, err := resolveRelationMeta(reflect.TypeOf(RelUser{}), "Nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent relation")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestResolveRelationMeta_ErrorNonStructField(t *testing.T) {
	_, err := resolveRelationMeta(reflect.TypeOf(BadRelationModel{}), "Tags")
	if err == nil {
		t.Fatal("expected error for non-struct relation field")
	}
	if !strings.Contains(err.Error(), "must be a struct") {
		t.Errorf("error should mention 'must be a struct', got: %v", err)
	}
}

func TestResolveRelationMeta_ErrorUnsafeIdentifier(t *testing.T) {
	_, err := resolveRelationMeta(reflect.TypeOf(UnsafeTagModel{}), "Items")
	if err == nil {
		t.Fatal("expected error for SQL-injection-like identifier")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention 'invalid', got: %v", err)
	}
}

func TestResolveRelationMeta_PointerModelType(t *testing.T) {
	// Passing *RelUser instead of RelUser should still work
	meta, err := resolveRelationMeta(reflect.TypeOf(&RelUser{}), "Posts")
	if err != nil {
		t.Fatalf("unexpected error with pointer type: %v", err)
	}
	if meta.relType != HasMany {
		t.Errorf("relType = %d, want HasMany", meta.relType)
	}
}

// ============================================================
// Unit tests: markIsExisting
// ============================================================

func TestMarkIsExisting(t *testing.T) {
	t.Run("SetsFlag", func(t *testing.T) {
		user := RelUser{}
		v := reflect.ValueOf(&user).Elem()
		markIsExisting(v)
		if !IsExisting(&user) {
			t.Error("expected IsExisting = true")
		}
	})

	t.Run("NoOpWithoutEmbeddedModel", func(t *testing.T) {
		// A plain struct with no embedded model — markIsExisting should not panic
		type Plain struct {
			Name string
		}
		p := Plain{Name: "test"}
		v := reflect.ValueOf(&p).Elem()
		markIsExisting(v) // should be a safe no-op
	})
}

// ============================================================
// Unit tests: structToMap skips relation fields
// ============================================================

func TestStructToMap_SkipsRelationFields(t *testing.T) {
	user := &RelUser{
		Name:  "Test",
		Posts: []RelPost{{Title: "should not appear"}},
	}
	user.Profile = &RelProfile{Bio: "should not appear"}

	data := structToMap(user)

	if _, ok := data["posts"]; ok {
		t.Error("structToMap should not include 'posts' (relation-tagged slice)")
	}
	if _, ok := data["profile"]; ok {
		t.Error("structToMap should not include 'profile' (relation-tagged pointer)")
	}
	if _, ok := data["name"]; !ok {
		t.Error("structToMap should include 'name' (normal field)")
	}
}

// ============================================================
// Integration tests: HasMany
// ============================================================

func TestLoadRelations_HasMany(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUser{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}

	// Alice: 2 posts, Bob: 1 post, Charlie: 0 posts
	expectations := map[string]int{"Alice": 2, "Bob": 1, "Charlie": 0}
	for _, user := range users {
		want, ok := expectations[user.Name]
		if !ok {
			t.Errorf("unexpected user %q", user.Name)
			continue
		}
		if len(user.Posts) != want {
			t.Errorf("%s: got %d posts, want %d", user.Name, len(user.Posts), want)
		}
	}
}

func TestLoadRelations_HasMany_ForeignKeyIntegrity(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUser{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	for _, user := range users {
		for _, post := range user.Posts {
			if post.UserID != user.Model.ID {
				t.Errorf("post %q (user_id=%d) assigned to user %q (id=%d)",
					post.Title, post.UserID, user.Name, user.Model.ID)
			}
		}
	}
}

func TestLoadRelations_HasMany_IsExistingSet(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUser{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	for ui := range users {
		user := &users[ui]
		for i := range user.Posts {
			if !IsExisting(&user.Posts[i]) {
				t.Errorf("user %q post[%d]: IsExisting should be true", user.Name, i)
			}
		}
	}
}

func TestLoadRelations_HasMany_PointerSlice(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUserPtrSlice{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	alice := findByName(users, "Alice")
	if alice == nil {
		t.Fatal("Alice not found")
	}
	if len(alice.Posts) != 2 {
		t.Fatalf("Alice: got %d posts, want 2", len(alice.Posts))
	}

	// Elements should be non-nil pointers
	for i, p := range alice.Posts {
		if p == nil {
			t.Errorf("Alice.Posts[%d] is nil", i)
		}
	}
}

// ============================================================
// Integration tests: HasOne
// ============================================================

func TestLoadRelations_HasOne(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUser{}.With("Profile").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	tests := []struct {
		name    string
		wantBio string
		wantNil bool
	}{
		{name: "Alice", wantBio: "Alice bio"},
		{name: "Bob", wantBio: "Bob bio"},
		{name: "Charlie", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := findByName(users, tt.name)
			if user == nil {
				t.Fatalf("%s not found", tt.name)
			}
			if tt.wantNil {
				if user.Profile != nil {
					t.Error("profile should be nil")
				}
				return
			}
			if user.Profile == nil {
				t.Fatal("profile should not be nil")
			}
			if user.Profile.Bio != tt.wantBio {
				t.Errorf("bio = %q, want %q", user.Profile.Bio, tt.wantBio)
			}
			if user.Profile.UserID != user.Model.ID {
				t.Errorf("profile.UserID = %d, want %d", user.Profile.UserID, user.Model.ID)
			}
			if !IsExisting(user.Profile) {
				t.Error("IsExisting(profile) should be true")
			}
		})
	}
}

// ============================================================
// Integration tests: BelongsTo
// ============================================================

func TestLoadRelations_BelongsTo(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	posts, err := RelPost{}.With("User").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(posts) != 3 {
		t.Fatalf("expected 3 posts, got %d", len(posts))
	}

	for pi := range posts {
		post := &posts[pi]
		if post.User == nil {
			t.Fatalf("post %q: user should not be nil", post.Title)
		}
		if post.User.Model.ID != post.UserID {
			t.Errorf("post %q: user.ID=%d, post.UserID=%d", post.Title, post.User.Model.ID, post.UserID)
		}
		if !IsExisting(post.User) {
			t.Errorf("post %q: IsExisting(user) should be true", post.Title)
		}
	}
}

func TestLoadRelations_BelongsTo_SharedParentDeduplication(t *testing.T) {
	// Alice has 2 posts. Both should resolve to the same user (Alice).
	// The IN query should deduplicate the user_id values.
	cleanup := withRelationDB(t)
	defer cleanup()

	posts, err := RelPost{}.With("User").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	alicePosts := 0
	for _, post := range posts {
		if post.UserID == 1 {
			alicePosts++
			if post.User == nil {
				t.Fatal("Alice's post should have a user")
			}
			if post.User.Name != "Alice" {
				t.Errorf("expected Alice, got %q", post.User.Name)
			}
		}
	}
	if alicePosts != 2 {
		t.Errorf("expected 2 posts belonging to Alice, got %d", alicePosts)
	}
}

func TestLoadRelations_BelongsTo_CorrectUserAssignment(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	posts, err := RelPost{}.With("User").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	wantNames := map[uint]string{1: "Alice", 2: "Bob"}
	for _, post := range posts {
		want := wantNames[post.UserID]
		if post.User.Name != want {
			t.Errorf("post.UserID=%d: user.Name=%q, want %q", post.UserID, post.User.Name, want)
		}
	}
}

// ============================================================
// Integration tests: multiple eager loads, chaining, filtering
// ============================================================

func TestLoadRelations_MultipleWith(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUser{}.With("Profile", "Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	alice := findByName(users, "Alice")
	if alice == nil {
		t.Fatal("Alice not found")
	}
	if alice.Profile == nil {
		t.Error("Alice should have a profile")
	}
	if len(alice.Posts) != 2 {
		t.Errorf("Alice should have 2 posts, got %d", len(alice.Posts))
	}

	charlie := findByName(users, "Charlie")
	if charlie == nil {
		t.Fatal("Charlie not found")
	}
	if charlie.Profile != nil {
		t.Error("Charlie should not have a profile")
	}
	if len(charlie.Posts) != 0 {
		t.Errorf("Charlie should have 0 posts, got %d", len(charlie.Posts))
	}
}

func TestLoadRelations_ChainedWithCalls(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// With().With() should accumulate preloads, not replace
	users, err := RelUser{}.With("Profile").With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	alice := findByName(users, "Alice")
	if alice == nil {
		t.Fatal("Alice not found")
	}
	if alice.Profile == nil {
		t.Error("chained With: profile should be loaded")
	}
	if len(alice.Posts) != 2 {
		t.Error("chained With: posts should be loaded")
	}
}

func TestLoadRelations_WithFirst(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	var user RelUser
	err := RelUser{}.With("Posts").Where("name = ?", "Alice").First(context.Background(), &user)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if user.Name != "Alice" {
		t.Errorf("name = %q, want Alice", user.Name)
	}
	if len(user.Posts) != 2 {
		t.Errorf("Alice should have 2 posts via First(), got %d", len(user.Posts))
	}
}

func TestLoadRelations_CaseInsensitive(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// lowercase "posts" should find the "Posts" field
	users, err := RelUser{}.With("posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	alice := findByName(users, "Alice")
	if alice == nil {
		t.Fatal("Alice not found")
	}
	if len(alice.Posts) != 2 {
		t.Errorf("case-insensitive With: got %d posts, want 2", len(alice.Posts))
	}
}

// ============================================================
// Integration tests: edge cases
// ============================================================

func TestLoadRelations_EmptyParentResults(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// Query returns 0 parents — loadRelations should be a no-op
	users, err := RelUser{}.With("Posts").Where("name = ?", "Nonexistent").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}
}

func TestLoadRelations_NoPreloads(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// No With() — relations should remain zero-valued
	users, err := RelUser{}.Where("id = ?", 1).Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Profile != nil {
		t.Error("Profile should be nil when not eager loaded")
	}
	if len(users[0].Posts) != 0 {
		t.Error("Posts should be empty when not eager loaded")
	}
}

func TestLoadRelations_ParentWithZeroFK(t *testing.T) {
	// Posts with user_id=0 should not trigger a query for user with id=0
	manager := setupRelationTables(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()

	// Insert a post with user_id=0 (orphan)
	_, err := db.Exec(`INSERT INTO rel_posts (id, user_id, title, created_at, updated_at) VALUES
		(1, 0, 'Orphan Post', '2024-01-01', '2024-01-01')`)
	if err != nil {
		t.Fatalf("failed to seed: %v", err)
	}

	SetDefault(manager)
	defer ResetDefault()

	posts, err := RelPost{}.With("User").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	if posts[0].User != nil {
		t.Error("post with user_id=0 should not have a user loaded")
	}
}

// ============================================================
// Integration tests: error paths
// ============================================================

func TestLoadRelations_ErrorInvalidRelation(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	_, err := RelUser{}.With("NonExistent").Get(context.Background())
	if err == nil {
		t.Fatal("expected error for non-existent relation")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestLoadRelations_ErrorUnsafeIdentifier(t *testing.T) {
	manager := setupRelationTables(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	// Seed a dummy row so loadRelations is actually called
	db := manager.DB()
	_, err := db.Exec(`CREATE TABLE unsafe_models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME,
		updated_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO unsafe_models (id, created_at, updated_at) VALUES (1, '2024-01-01', '2024-01-01')`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// UnsafeTagModel has a SQL-injection-like FK identifier — resolveRelationMeta should reject it
	_, err = UnsafeTagModel{}.With("Items").Get(context.Background())
	if err == nil {
		t.Fatal("expected error for unsafe identifier in relation tag")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention 'invalid', got: %v", err)
	}
}

// ============================================================
// Integration tests: soft deletes
// ============================================================

func TestLoadRelations_SoftDeleteFiltering(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	db := manager.DB()

	for _, ddl := range []string{
		`CREATE TABLE soft_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE soft_posts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL, title TEXT NOT NULL,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	_, err := db.Exec(`INSERT INTO soft_users (id, name, created_at, updated_at) VALUES (1, 'Alice', '2024-01-01', '2024-01-01')`)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO soft_posts (id, user_id, title, created_at, updated_at, deleted_at) VALUES
		(1, 1, 'Active 1', '2024-01-01', '2024-01-01', NULL),
		(2, 1, 'Active 2', '2024-01-01', '2024-01-01', NULL),
		(3, 1, 'Deleted', '2024-01-01', '2024-01-01', '2024-06-01')`)
	if err != nil {
		t.Fatalf("seed posts: %v", err)
	}

	SetDefault(manager)
	defer ResetDefault()

	users, err := SoftUser{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if len(users[0].Posts) != 2 {
		t.Errorf("expected 2 active posts, got %d", len(users[0].Posts))
	}
	for _, p := range users[0].Posts {
		if p.Title == "Deleted" {
			t.Error("soft-deleted post should not be included in eager load")
		}
	}
}

// ============================================================
// Concurrency test
// ============================================================

func TestLoadRelations_Concurrent(t *testing.T) {
	manager := setupRelationTables(t)
	defer manager.Shutdown(context.Background())
	// SQLite :memory: creates separate DBs per connection.
	// Limit to 1 so concurrent goroutines share the same database.
	manager.DB().SetMaxOpenConns(1)
	seedRelationData(t, manager)
	SetDefault(manager)
	defer ResetDefault()

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			users, err := RelUser{}.With("Posts", "Profile").Get(context.Background())
			if err != nil {
				errCh <- fmt.Errorf("Get: %w", err)
				return
			}
			if len(users) != 3 {
				errCh <- fmt.Errorf("expected 3 users, got %d", len(users))
				return
			}

			alice := findByName(users, "Alice")
			if alice == nil {
				errCh <- fmt.Errorf("Alice not found")
				return
			}
			if len(alice.Posts) != 2 {
				errCh <- fmt.Errorf("Alice posts: got %d, want 2", len(alice.Posts))
				return
			}
			if alice.Profile == nil {
				errCh <- fmt.Errorf("Alice profile is nil")
				return
			}
			if alice.Profile.Bio != "Alice bio" {
				errCh <- fmt.Errorf("Alice profile bio = %q, want 'Alice bio'", alice.Profile.Bio)
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent: %v", err)
	}
}

func TestLoadRelations_ConcurrentBelongsTo(t *testing.T) {
	manager := setupRelationTables(t)
	defer manager.Shutdown(context.Background())
	manager.DB().SetMaxOpenConns(1)
	seedRelationData(t, manager)
	SetDefault(manager)
	defer ResetDefault()

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			posts, err := RelPost{}.With("User").Get(context.Background())
			if err != nil {
				errCh <- fmt.Errorf("Get: %w", err)
				return
			}
			if len(posts) != 3 {
				errCh <- fmt.Errorf("expected 3 posts, got %d", len(posts))
				return
			}
			for _, post := range posts {
				if post.User == nil {
					errCh <- fmt.Errorf("post %q: user is nil", post.Title)
					return
				}
				if post.User.Model.ID != post.UserID {
					errCh <- fmt.Errorf("post %q: user.ID=%d != UserID=%d",
						post.Title, post.User.Model.ID, post.UserID)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent: %v", err)
	}
}

// ============================================================
// Helpers
// ============================================================

func findByName[T interface{ GetName() string }](items []T, name string) *T {
	for i := range items {
		if items[i].GetName() == name {
			return &items[i]
		}
	}
	return nil
}

func (u RelUser) GetName() string         { return u.Name }
func (u RelUserPtrSlice) GetName() string { return u.Name }
func (u SoftUser) GetName() string        { return u.Name }
