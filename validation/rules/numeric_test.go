package rules

import (
	"testing"
)

func TestIntegerRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "count",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int type",
			field:   "count",
			value:   42,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative int",
			field:   "count",
			value:   -10,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for zero int",
			field:   "count",
			value:   0,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int8 type",
			field:   "count",
			value:   int8(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int16 type",
			field:   "count",
			value:   int16(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int32 type",
			field:   "count",
			value:   int32(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int64 type",
			field:   "count",
			value:   int64(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint type",
			field:   "count",
			value:   uint(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint8 type",
			field:   "count",
			value:   uint8(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint16 type",
			field:   "count",
			value:   uint16(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint32 type",
			field:   "count",
			value:   uint32(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint64 type",
			field:   "count",
			value:   uint64(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for whole number float64",
			field:   "count",
			value:   float64(5.0),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative whole number float64",
			field:   "count",
			value:   float64(-10.0),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for zero float64",
			field:   "count",
			value:   float64(0.0),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for fractional float64",
			field:   "count",
			value:   float64(5.5),
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for small fractional float64",
			field:   "count",
			value:   float64(5.1),
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil for valid integer string",
			field:   "count",
			value:   "42",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative integer string",
			field:   "count",
			value:   "-10",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for float string",
			field:   "count",
			value:   "5.5",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for non-numeric string",
			field:   "count",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "count",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for bool type",
			field:   "count",
			value:   true,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for slice type",
			field:   "count",
			value:   []int{1, 2, 3},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IntegerRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("IntegerRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNumericRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "price",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int type",
			field:   "price",
			value:   42,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative int",
			field:   "price",
			value:   -10,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int8 type",
			field:   "price",
			value:   int8(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int16 type",
			field:   "price",
			value:   int16(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int32 type",
			field:   "price",
			value:   int32(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int64 type",
			field:   "price",
			value:   int64(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint type",
			field:   "price",
			value:   uint(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint8 type",
			field:   "price",
			value:   uint8(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint16 type",
			field:   "price",
			value:   uint16(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint32 type",
			field:   "price",
			value:   uint32(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uint64 type",
			field:   "price",
			value:   uint64(42),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for float32 type",
			field:   "price",
			value:   float32(3.14),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for float64 type",
			field:   "price",
			value:   float64(3.14159),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative float64",
			field:   "price",
			value:   float64(-10.5),
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for valid integer string",
			field:   "price",
			value:   "42",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for valid float string",
			field:   "price",
			value:   "3.14",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for negative numeric string",
			field:   "price",
			value:   "-10.5",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for scientific notation string",
			field:   "price",
			value:   "1.5e10",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for non-numeric string",
			field:   "price",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with letters and numbers",
			field:   "price",
			value:   "42abc",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "price",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for bool type",
			field:   "price",
			value:   true,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for slice type",
			field:   "price",
			value:   []int{1, 2, 3},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NumericRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("NumericRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBooleanRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "active",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for bool true",
			field:   "active",
			value:   true,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for bool false",
			field:   "active",
			value:   false,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string true lowercase",
			field:   "active",
			value:   "true",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string false lowercase",
			field:   "active",
			value:   "false",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for string True with capital T",
			field:   "active",
			value:   "True",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string FALSE uppercase",
			field:   "active",
			value:   "FALSE",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string TRUE uppercase",
			field:   "active",
			value:   "TRUE",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil for string 1",
			field:   "active",
			value:   "1",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string 0",
			field:   "active",
			value:   "0",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string yes lowercase",
			field:   "active",
			value:   "yes",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string no lowercase",
			field:   "active",
			value:   "no",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for string Yes with capital Y",
			field:   "active",
			value:   "Yes",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string NO uppercase",
			field:   "active",
			value:   "NO",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string on",
			field:   "active",
			value:   "on",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string off",
			field:   "active",
			value:   "off",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil for int 0",
			field:   "active",
			value:   0,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int 1",
			field:   "active",
			value:   1,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for int 2",
			field:   "active",
			value:   2,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for int -1",
			field:   "active",
			value:   -1,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for float type",
			field:   "active",
			value:   1.0,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for slice type",
			field:   "active",
			value:   []bool{true},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "active",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BooleanRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("BooleanRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestArrayRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "items",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for empty interface slice",
			field:   "items",
			value:   []interface{}{},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for interface slice with elements",
			field:   "items",
			value:   []interface{}{"a", 1, true},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for empty string slice",
			field:   "items",
			value:   []string{},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for string slice with elements",
			field:   "items",
			value:   []string{"a", "b", "c"},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for empty int slice",
			field:   "items",
			value:   []int{},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for int slice with elements",
			field:   "items",
			value:   []int{1, 2, 3},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for empty float64 slice",
			field:   "items",
			value:   []float64{},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for float64 slice with elements",
			field:   "items",
			value:   []float64{1.1, 2.2, 3.3},
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for bool slice",
			field:   "items",
			value:   []bool{true, false},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for uint slice",
			field:   "items",
			value:   []uint{1, 2, 3},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for int8 slice",
			field:   "items",
			value:   []int8{1, 2, 3},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for float32 slice",
			field:   "items",
			value:   []float32{1.1, 2.2},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string type",
			field:   "items",
			value:   "not an array",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for int type",
			field:   "items",
			value:   42,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for map type",
			field:   "items",
			value:   map[string]interface{}{"key": "value"},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for struct type",
			field:   "items",
			value:   struct{ Name string }{Name: "test"},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ArrayRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ArrayRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
