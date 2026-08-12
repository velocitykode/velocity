package orm

import (
	"reflect"
	"testing"
)

// Models for JSON/JSONB skip-on-zero tests. Each isolates a single
// type/tag combo so failures point straight at the offending branch.

type jsonbStringModel struct {
	Model[jsonbStringModel]
	Name     string `orm:"column:name"`
	Settings string `orm:"column:settings;type:jsonb;not_null;default:{}"`
}

func (jsonbStringModel) TableName() string { return "jsonb_string_models" }

func (jsonbStringModel) AssignableFields() []string { return []string{"name", "settings"} }

type jsonStringModel struct {
	Model[jsonStringModel]
	Name string `orm:"column:name"`
	Meta string `orm:"column:meta;type:json"`
}

func (jsonStringModel) TableName() string { return "json_string_models" }

type jsonbPtrModel struct {
	Model[jsonbPtrModel]
	Name     string  `orm:"column:name"`
	Settings *string `orm:"column:settings;type:jsonb"`
}

func (jsonbPtrModel) TableName() string { return "jsonb_ptr_models" }

type jsonbBytesModel struct {
	Model[jsonbBytesModel]
	Name     string `orm:"column:name"`
	Settings []byte `orm:"column:settings;type:jsonb"`
}

func (jsonbBytesModel) TableName() string { return "jsonb_bytes_models" }

type jsonbMapModel struct {
	Model[jsonbMapModel]
	Name     string         `orm:"column:name"`
	Settings map[string]any `orm:"column:settings;type:jsonb"`
}

func (jsonbMapModel) TableName() string { return "jsonb_map_models" }

type jsonbSliceModel struct {
	Model[jsonbSliceModel]
	Name string `orm:"column:name"`
	Tags []any  `orm:"column:tags;type:jsonb"`
}

func (jsonbSliceModel) TableName() string { return "jsonb_slice_models" }

// Confirms tag parser does not misfire on partial-substring type names.
type jsonbLookalikeModel struct {
	Model[jsonbLookalikeModel]
	Name  string `orm:"column:name"`
	Other string `orm:"column:other;type:jsonb_packed"`
}

func (jsonbLookalikeModel) TableName() string { return "jsonb_lookalike_models" }

func TestStructToMap_JSONBStringZeroOmitted(t *testing.T) {
	m := &jsonbStringModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings omitted when zero, got %v", data["settings"])
	}
	if data["name"] != "x" {
		t.Fatalf("expected name=x, got %v", data["name"])
	}
}

func TestStructToMap_JSONStringZeroOmitted(t *testing.T) {
	m := &jsonStringModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["meta"]; ok {
		t.Fatalf("expected meta omitted for type:json zero, got %v", data["meta"])
	}
}

func TestStructToMap_JSONBStringExplicitValueRetained(t *testing.T) {
	m := &jsonbStringModel{Name: "x", Settings: "{}"}
	data := structToMap(m)
	v, ok := data["settings"]
	if !ok {
		t.Fatal("expected settings included when caller set '{}'")
	}
	if v != "{}" {
		t.Fatalf("expected settings='{}', got %v", v)
	}
}

func TestStructToMap_JSONBStringNonEmptyRetained(t *testing.T) {
	m := &jsonbStringModel{Name: "x", Settings: `{"foo":1}`}
	data := structToMap(m)
	if data["settings"] != `{"foo":1}` {
		t.Fatalf("expected settings forwarded verbatim, got %v", data["settings"])
	}
}

func TestStructToMap_JSONBPtrNilOmitted(t *testing.T) {
	m := &jsonbPtrModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings omitted for nil *string, got %v", data["settings"])
	}
}

func TestStructToMap_JSONBPtrNonNilRetained(t *testing.T) {
	val := "{}"
	m := &jsonbPtrModel{Name: "x", Settings: &val}
	data := structToMap(m)
	got, ok := data["settings"]
	if !ok {
		t.Fatal("expected settings included for non-nil *string")
	}
	if reflect.ValueOf(got).Elem().String() != "{}" {
		t.Fatalf("expected pointer to '{}', got %v", got)
	}
}

func TestStructToMap_JSONBBytesNilOmitted(t *testing.T) {
	m := &jsonbBytesModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings omitted for nil []byte, got %v", data["settings"])
	}
}

func TestStructToMap_JSONBBytesEmptyOmitted(t *testing.T) {
	m := &jsonbBytesModel{Name: "x", Settings: []byte{}}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings omitted for empty []byte, got %v", data["settings"])
	}
}

func TestStructToMap_JSONBBytesNonEmptyRetained(t *testing.T) {
	m := &jsonbBytesModel{Name: "x", Settings: []byte(`{"a":1}`)}
	data := structToMap(m)
	got, ok := data["settings"]
	if !ok {
		t.Fatal("expected settings included for non-empty []byte")
	}
	if string(got.([]byte)) != `{"a":1}` {
		t.Fatalf("expected settings forwarded verbatim, got %s", got)
	}
}

func TestStructToMap_JSONBMapNilOmitted(t *testing.T) {
	m := &jsonbMapModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings omitted for nil map, got %v", data["settings"])
	}
}

func TestStructToMap_JSONBMapEmptyRetained(t *testing.T) {
	// Empty map = explicit user intent ('{}'), distinct from nil.
	m := &jsonbMapModel{Name: "x", Settings: map[string]any{}}
	data := structToMap(m)
	got, ok := data["settings"]
	if !ok {
		t.Fatal("expected settings retained for empty map (user-supplied {})")
	}
	if got == nil {
		t.Fatal("expected non-nil map retained")
	}
	if v := reflect.ValueOf(got); v.Kind() != reflect.Map || v.Len() != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestStructToMap_JSONBMapPopulatedRetained(t *testing.T) {
	m := &jsonbMapModel{Name: "x", Settings: map[string]any{"foo": 1}}
	data := structToMap(m)
	got, ok := data["settings"]
	if !ok {
		t.Fatal("expected settings retained for populated map")
	}
	v := reflect.ValueOf(got)
	if v.Kind() != reflect.Map || v.Len() != 1 {
		t.Fatalf("expected 1-key map, got %v", got)
	}
}

func TestStructToMap_JSONBSliceNilOmitted(t *testing.T) {
	m := &jsonbSliceModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["tags"]; ok {
		t.Fatalf("expected tags omitted for nil slice, got %v", data["tags"])
	}
}

func TestStructToMap_JSONBSliceEmptyRetained(t *testing.T) {
	// Empty []any = explicit user intent ('[]'), distinct from nil.
	m := &jsonbSliceModel{Name: "x", Tags: []any{}}
	data := structToMap(m)
	got, ok := data["tags"]
	if !ok {
		t.Fatal("expected tags retained for empty slice (user-supplied [])")
	}
	if v := reflect.ValueOf(got); v.Kind() != reflect.Slice || v.Len() != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestStructToMap_JSONBLookalikeTagIgnored(t *testing.T) {
	// type:jsonb_packed is NOT type:jsonb. Field is plain string, treated
	// like any other column: zero string is included as "". This proves the
	// tag parser anchors on ';' boundaries.
	m := &jsonbLookalikeModel{Name: "x"}
	data := structToMap(m)
	v, ok := data["other"]
	if !ok {
		t.Fatal("expected lookalike-tagged column to be included as plain string")
	}
	if v != "" {
		t.Fatalf("expected empty string included verbatim, got %v", v)
	}
}

func TestStructToMap_NonJSONStringZeroStillIncluded(t *testing.T) {
	// Regression: non-JSON string fields with zero value are still included
	// (existing behavior). Skip-on-zero is JSON-only.
	u := &TestUser{Name: ""}
	data := structToMap(u)
	if _, ok := data["name"]; !ok {
		t.Fatal("expected non-JSON empty string still included")
	}
}

// Verify Create(map) path: caller supplies value via data map, struct
// introspection forwards it. Round-trips through mapToStruct + structToMap.
func TestCreateMapPath_JSONBValueWins(t *testing.T) {
	m := &jsonbStringModel{}
	if err := mapToStruct(map[string]any{"name": "x", "settings": "{}"}, m); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	data := structToMap(m)
	if data["settings"] != "{}" {
		t.Fatalf("expected settings='{}' from data map, got %v", data["settings"])
	}
}

func TestCreateMapPath_JSONBOmittedKeyOmittedFromInsert(t *testing.T) {
	m := &jsonbStringModel{}
	if err := mapToStruct(map[string]any{"name": "x"}, m); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	data := structToMap(m)
	if _, ok := data["settings"]; ok {
		t.Fatalf("expected settings absent when caller omitted key, got %v", data["settings"])
	}
}

// Direct unit tests for the helpers.
func TestOrmTagType(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"", ""},
		{"column:foo", ""},
		{"column:foo;type:jsonb;not_null", "jsonb"},
		{"type:json", "json"},
		{"type:jsonb_packed", "jsonb_packed"},
		{"column:foo;not_null", ""},
	}
	for _, tc := range tests {
		if got := ormTagType(tc.tag); got != tc.want {
			t.Errorf("ormTagType(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestIsJSONColumn(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"type:json", true},
		{"type:jsonb", true},
		{"column:x;type:jsonb;not_null", true},
		{"type:jsonb_packed", false},
		{"type:text", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isJSONColumn(tc.tag); got != tc.want {
			t.Errorf("isJSONColumn(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}
