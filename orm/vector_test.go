package orm

import (
	"database/sql/driver"
	"reflect"
	"testing"
)

func TestVector_Value(t *testing.T) {
	tests := []struct {
		name string
		v    Vector
		want any
	}{
		{"nil is NULL", nil, nil},
		{"empty", Vector{}, "[]"},
		{"ints", Vector{1, 2, 3}, "[1,2,3]"},
		{"fractional", Vector{0.5, -1.25}, "[0.5,-1.25]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.v.Value()
			if err != nil {
				t.Fatalf("Value() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Value() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// Vector must satisfy the database/sql interfaces so round-tripping needs no
// special-casing in the hydration path.
func TestVector_ImplementsSQLInterfaces(t *testing.T) {
	var _ driver.Valuer = Vector(nil)
	var _ interface{ Scan(any) error } = (*Vector)(nil)
}

func TestVector_Scan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    Vector
		wantErr bool
	}{
		{"nil", nil, nil, false},
		{"string", "[1,2,3]", Vector{1, 2, 3}, false},
		{"bytes", []byte("[1.5,2.5]"), Vector{1.5, 2.5}, false},
		{"whitespace tolerated", " [ 1 , 2 ] ", Vector{1, 2}, false},
		{"empty literal", "[]", Vector{}, false},
		{"malformed no brackets", "1,2,3", nil, true},
		{"malformed element", "[1,x,3]", nil, true},
		{"wrong type", 42, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v Vector
			err := v.Scan(tt.src)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Scan(%#v) expected error, got nil", tt.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan(%#v) error = %v", tt.src, err)
			}
			if !reflect.DeepEqual(v, tt.want) {
				t.Errorf("Scan(%#v) = %#v, want %#v", tt.src, v, tt.want)
			}
		})
	}
}

type vecWriteModel struct {
	Model[vecWriteModel]
	Embedding Vector `orm:"type:vector(3)"`
}

func (vecWriteModel) TableName() string { return "vec_docs" }

// structToMap previously dropped every non-byte slice as a relation payload. A
// Vector implements driver.Valuer, so the write path must emit it (the driver
// serializes it at bind time) rather than silently omitting it from the INSERT.
func TestStructToMap_EmitsVector(t *testing.T) {
	m := vecWriteModel{Embedding: Vector{1, 2, 3}}
	out := structToMap(&m)
	v, ok := out["embedding"]
	if !ok {
		t.Fatal("embedding column not emitted: a Vector field was dropped on write")
	}
	vec, ok := v.(Vector)
	if !ok || !reflect.DeepEqual(vec, Vector{1, 2, 3}) {
		t.Errorf("emitted value = %#v, want Vector{1,2,3}", v)
	}
}

// The canonical round-trip: a value written via Value() must read back equal
// via Scan(), since that is exactly how the column moves through the driver.
func TestVector_RoundTrip(t *testing.T) {
	orig := Vector{0.1, -0.2, 3.5, 0}
	encoded, err := orig.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	var back Vector
	if err := back.Scan(encoded.(string)); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if !reflect.DeepEqual(orig, back) {
		t.Errorf("round-trip = %#v, want %#v", back, orig)
	}
}
