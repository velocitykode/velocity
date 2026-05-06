package orm

import (
	"bytes"
	"testing"
)

// Models exercising binary-scalar slice/array handling in structToMap.
// Each isolates a single shape so failures point straight at the offending
// branch.

type byteaModel struct {
	Model[byteaModel]
	Name string `orm:"column:name"`
	Hash []byte `orm:"column:hash;type:bytea;not_null"`
}

func (byteaModel) TableName() string { return "bytea_models" }

type byteaArrayModel struct {
	Model[byteaArrayModel]
	Name   string   `orm:"column:name"`
	Digest [32]byte `orm:"column:digest;type:bytea;not_null"`
}

func (byteaArrayModel) TableName() string { return "bytea_array_models" }

type relationSliceModel struct {
	Model[relationSliceModel]
	Name string `orm:"column:name"`
	// No tag: exercises the slice-skip branch in structToMap rather than
	// the early `orm:"-"` exit, which is what we want to assert.
	Children []relationChildRow
}

type relationChildRow struct {
	ID uint
}

func (relationSliceModel) TableName() string { return "relation_slice_models" }

type jsonStringSliceModel struct {
	Model[jsonStringSliceModel]
	Name string   `orm:"column:name"`
	Tags []string `orm:"column:tags;type:jsonb"`
}

func (jsonStringSliceModel) TableName() string { return "json_string_slice_models" }

func TestStructToMap_ByteSlicePopulated(t *testing.T) {
	m := &byteaModel{Name: "x", Hash: []byte{0x01, 0x02, 0x03}}
	data := structToMap(m)
	v, ok := data["hash"]
	if !ok {
		t.Fatal("expected hash included for non-nil []byte on bytea column")
	}
	got, ok := v.([]byte)
	if !ok {
		t.Fatalf("expected []byte value, got %T", v)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("expected verbatim bytes, got %v", got)
	}
}

func TestStructToMap_ByteSliceEmptyNonNilRetained(t *testing.T) {
	m := &byteaModel{Name: "x", Hash: []byte{}}
	data := structToMap(m)
	v, ok := data["hash"]
	if !ok {
		t.Fatal("expected hash included for non-nil empty []byte (write empty bytea)")
	}
	got, ok := v.([]byte)
	if !ok {
		t.Fatalf("expected []byte value, got %T", v)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero-length []byte, got len=%d", len(got))
	}
}

func TestStructToMap_ByteSliceNilOmitted(t *testing.T) {
	m := &byteaModel{Name: "x"}
	data := structToMap(m)
	if _, ok := data["hash"]; ok {
		t.Fatalf("expected hash omitted for nil []byte so DB default applies, got %v", data["hash"])
	}
	if data["name"] != "x" {
		t.Fatalf("expected name=x, got %v", data["name"])
	}
}

func TestStructToMap_FixedByteArrayPassedThrough(t *testing.T) {
	var digest [32]byte
	for i := range digest {
		digest[i] = byte(i)
	}
	m := &byteaArrayModel{Name: "x", Digest: digest}
	data := structToMap(m)
	v, ok := data["digest"]
	if !ok {
		t.Fatal("expected digest included for [32]byte on bytea column")
	}
	got, ok := v.([32]byte)
	if !ok {
		t.Fatalf("expected [32]byte value, got %T", v)
	}
	if got != digest {
		t.Fatalf("expected verbatim digest, got %v", got)
	}
}

func TestStructToMap_RelationSliceStillSkipped(t *testing.T) {
	m := &relationSliceModel{
		Name:     "x",
		Children: []relationChildRow{{ID: 1}, {ID: 2}},
	}
	data := structToMap(m)
	if _, ok := data["children"]; ok {
		t.Fatal("expected non-byte relation slice omitted from row payload")
	}
	if data["name"] != "x" {
		t.Fatalf("expected name=x, got %v", data["name"])
	}
}

func TestStructToMap_JSONByteSliceRetained(t *testing.T) {
	m := &jsonbBytesModel{Name: "x", Settings: []byte(`{"foo":1}`)}
	data := structToMap(m)
	v, ok := data["settings"]
	if !ok {
		t.Fatal("expected JSON-tagged []byte to flow through")
	}
	if !bytes.Equal(v.([]byte), []byte(`{"foo":1}`)) {
		t.Fatalf("expected verbatim JSON bytes, got %v", v)
	}
}

func TestStructToMap_JSONNonByteSliceRetained(t *testing.T) {
	m := &jsonStringSliceModel{Name: "x", Tags: []string{"a", "b"}}
	data := structToMap(m)
	v, ok := data["tags"]
	if !ok {
		t.Fatal("expected JSON-tagged []string to flow through")
	}
	got, ok := v.([]string)
	if !ok {
		t.Fatalf("expected []string value, got %T", v)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected verbatim tags, got %v", got)
	}
}
