package orm

import (
	"testing"
	"time"
)

// panickingInt, panickingText and panickingBytes are driver-specific
// values a NamedValueChecker could let through, with methods that panic:
// the binding is read from their kind, so none of the methods runs inside
// the driver callback.
type panickingInt int32

func (panickingInt) String() string { panic("String called") }

type panickingText string

func (panickingText) String() string { panic("String called") }

type panickingBytes []byte

func (panickingBytes) String() string { panic("String called") }

type panickingStruct struct{ V int }

func (panickingStruct) String() string { panic("String called") }

// Each bound value becomes its driver type and its text, read from its
// kind: never through a method of the value.
func TestQueryBindings(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC)
	n := 5
	cases := []struct {
		name string
		in   any
		want QueryBinding
	}{
		{"nil", nil, QueryBinding{}},
		{"int64", int64(-42), QueryBinding{Type: "int64", Value: "-42"}},
		{"float64", 3.25, QueryBinding{Type: "float64", Value: "3.25"}},
		{"bool", true, QueryBinding{Type: "bool", Value: "true"}},
		{"string", "alice", QueryBinding{Type: "string", Value: "alice"}},
		{"bytes are hex", []byte{0x00, 0xff, 'a'}, QueryBinding{Type: "[]uint8", Value: "00ff61"}},
		{"empty bytes", []byte{}, QueryBinding{Type: "[]uint8", Value: ""}},
		{"time", at, QueryBinding{Type: "time.Time", Value: "2026-09-30T12:00:00.123456789Z"}},
		{"uint64", uint64(1 << 63), QueryBinding{Type: "uint64", Value: "9223372036854775808"}},
		{"float32", float32(1.5), QueryBinding{Type: "float32", Value: "1.5"}},
		{"named int", panickingInt(7), QueryBinding{Type: "orm.panickingInt", Value: "7"}},
		{"named string", panickingText("x"), QueryBinding{Type: "orm.panickingText", Value: "x"}},
		{"named bytes", panickingBytes("ab"), QueryBinding{Type: "orm.panickingBytes", Value: "6162"}},
		{"struct", panickingStruct{V: 1}, QueryBinding{Type: "orm.panickingStruct"}},
		{"pointer", &n, QueryBinding{Type: "*int"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queryBinding(tc.in); got != tc.want {
				t.Errorf("queryBinding(%T) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
	if got := queryBindings(nil); got != nil {
		t.Errorf("queryBindings(nil) = %#v, want nil", got)
	}
	if got := queryBindings([]any{int64(1), nil}); len(got) != 2 || got[0].Value != "1" || got[1] != (QueryBinding{}) {
		t.Errorf("queryBindings = %#v, want [int64 1, NULL]", got)
	}
}
