package rules

import (
	"testing"
)

func TestStringRule(t *testing.T) {
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
			field:   "name",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when value is a string",
			field:   "name",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when value is empty string",
			field:   "name",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when value is int",
			field:   "name",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is float",
			field:   "name",
			value:   3.14,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is bool",
			field:   "name",
			value:   true,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is slice",
			field:   "name",
			value:   []string{"a", "b"},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := StringRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("StringRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEmailRule(t *testing.T) {
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
			field:   "email",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for valid email",
			field:   "email",
			value:   "user@example.com",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for email with plus sign",
			field:   "email",
			value:   "user+tag@example.com",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for email with dots",
			field:   "email",
			value:   "first.last@example.co.uk",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for email without at sign",
			field:   "email",
			value:   "userexample.com",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for email without domain",
			field:   "email",
			value:   "user@",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for email without TLD",
			field:   "email",
			value:   "user@example",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "email",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is not a string",
			field:   "email",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EmailRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("EmailRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestURLRule(t *testing.T) {
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
			field:   "website",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for valid http URL",
			field:   "website",
			value:   "http://example.com",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for valid https URL",
			field:   "website",
			value:   "https://example.com",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for URL with path",
			field:   "website",
			value:   "https://example.com/path/to/page",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for URL with query string",
			field:   "website",
			value:   "https://example.com?query=value",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for ftp URL",
			field:   "website",
			value:   "ftp://example.com",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for URL without scheme",
			field:   "website",
			value:   "example.com",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "website",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is not a string",
			field:   "website",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := URLRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("URLRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAlphaRule(t *testing.T) {
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
			field:   "name",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for lowercase letters",
			field:   "name",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for uppercase letters",
			field:   "name",
			value:   "HELLO",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for mixed case letters",
			field:   "name",
			value:   "HelloWorld",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for string with numbers",
			field:   "name",
			value:   "hello123",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with spaces",
			field:   "name",
			value:   "hello world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with special characters",
			field:   "name",
			value:   "hello-world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "name",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is not a string",
			field:   "name",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AlphaRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("AlphaRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAlphaDashRule(t *testing.T) {
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
			field:   "slug",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters only",
			field:   "slug",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters and numbers",
			field:   "slug",
			value:   "hello123",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters with dashes",
			field:   "slug",
			value:   "hello-world",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters with underscores",
			field:   "slug",
			value:   "hello_world",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for mixed alphanumeric with dash and underscore",
			field:   "slug",
			value:   "Hello_World-123",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for string with at sign",
			field:   "slug",
			value:   "user@domain",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with spaces",
			field:   "slug",
			value:   "hello world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "slug",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is not a string",
			field:   "slug",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AlphaDashRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("AlphaDashRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAlphaNumRule(t *testing.T) {
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
			field:   "code",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters only",
			field:   "code",
			value:   "hello",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for numbers only",
			field:   "code",
			value:   "123",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil for letters and numbers",
			field:   "code",
			value:   "abc123",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error for string with dashes",
			field:   "code",
			value:   "hello-world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with underscores",
			field:   "code",
			value:   "hello_world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for string with spaces",
			field:   "code",
			value:   "hello world",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for empty string",
			field:   "code",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is not a string",
			field:   "code",
			value:   123,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AlphaNumRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("AlphaNumRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMinRule(t *testing.T) {
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
			field:   "name",
			value:   nil,
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string length equals min",
			field:   "name",
			value:   "abc",
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string length exceeds min",
			field:   "name",
			value:   "abcdef",
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string length is below min",
			field:   "name",
			value:   "ab",
			params:  []string{"3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when int equals min",
			field:   "age",
			value:   18,
			params:  []string{"18"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int exceeds min",
			field:   "age",
			value:   25,
			params:  []string{"18"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when int is below min",
			field:   "age",
			value:   16,
			params:  []string{"18"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when float64 exceeds min",
			field:   "price",
			value:   10.5,
			params:  []string{"10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when float64 is below min",
			field:   "price",
			value:   5.5,
			params:  []string{"10"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when interface slice length equals min",
			field:   "items",
			value:   []interface{}{"a", "b", "c"},
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when interface slice length is below min",
			field:   "items",
			value:   []interface{}{"a"},
			params:  []string{"3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when string slice length equals min",
			field:   "tags",
			value:   []string{"a", "b"},
			params:  []string{"2"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string slice length is below min",
			field:   "tags",
			value:   []string{"a"},
			params:  []string{"2"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "name",
			value:   "hello",
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when param is not a number",
			field:   "name",
			value:   "hello",
			params:  []string{"abc"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for unsupported type",
			field:   "data",
			value:   struct{}{},
			params:  []string{"1"},
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MinRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("MinRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMaxRule(t *testing.T) {
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
			field:   "name",
			value:   nil,
			params:  []string{"10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string length equals max",
			field:   "name",
			value:   "abcdefghij",
			params:  []string{"10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string length is below max",
			field:   "name",
			value:   "abc",
			params:  []string{"10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string length exceeds max",
			field:   "name",
			value:   "abcdefghijk",
			params:  []string{"10"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when int equals max",
			field:   "age",
			value:   100,
			params:  []string{"100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int is below max",
			field:   "age",
			value:   50,
			params:  []string{"100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when int exceeds max",
			field:   "age",
			value:   101,
			params:  []string{"100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when float64 is below max",
			field:   "price",
			value:   99.99,
			params:  []string{"100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when float64 exceeds max",
			field:   "price",
			value:   100.01,
			params:  []string{"100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when interface slice length equals max",
			field:   "items",
			value:   []interface{}{"a", "b", "c"},
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when interface slice length exceeds max",
			field:   "items",
			value:   []interface{}{"a", "b", "c", "d"},
			params:  []string{"3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when string slice length is below max",
			field:   "tags",
			value:   []string{"a", "b"},
			params:  []string{"5"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string slice length exceeds max",
			field:   "tags",
			value:   []string{"a", "b", "c", "d", "e", "f"},
			params:  []string{"5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "name",
			value:   "hello",
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when param is not a number",
			field:   "name",
			value:   "hello",
			params:  []string{"abc"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for unsupported type",
			field:   "data",
			value:   struct{}{},
			params:  []string{"10"},
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MaxRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("MaxRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSizeRule(t *testing.T) {
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
			field:   "code",
			value:   nil,
			params:  []string{"5"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string length equals size",
			field:   "code",
			value:   "abcde",
			params:  []string{"5"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string length is below size",
			field:   "code",
			value:   "abc",
			params:  []string{"5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when string length exceeds size",
			field:   "code",
			value:   "abcdefg",
			params:  []string{"5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when int equals size",
			field:   "count",
			value:   10,
			params:  []string{"10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when int does not equal size",
			field:   "count",
			value:   11,
			params:  []string{"10"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when interface slice length equals size",
			field:   "items",
			value:   []interface{}{"a", "b", "c"},
			params:  []string{"3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when interface slice length does not equal size",
			field:   "items",
			value:   []interface{}{"a", "b"},
			params:  []string{"3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when string slice length equals size",
			field:   "tags",
			value:   []string{"a", "b"},
			params:  []string{"2"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string slice length does not equal size",
			field:   "tags",
			value:   []string{"a", "b", "c"},
			params:  []string{"2"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for float64 type",
			field:   "price",
			value:   5.0,
			params:  []string{"5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "code",
			value:   "hello",
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when param is not a number",
			field:   "code",
			value:   "hello",
			params:  []string{"abc"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for unsupported type",
			field:   "data",
			value:   struct{}{},
			params:  []string{"1"},
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SizeRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("SizeRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBetweenRule(t *testing.T) {
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
			field:   "age",
			value:   nil,
			params:  []string{"1", "10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when numeric string is within range",
			field:   "code",
			value:   "50",
			params:  []string{"1", "100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when numeric string equals min",
			field:   "code",
			value:   "1",
			params:  []string{"1", "100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when numeric string equals max",
			field:   "code",
			value:   "100",
			params:  []string{"1", "100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when numeric string is below min",
			field:   "code",
			value:   "0",
			params:  []string{"1", "100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when numeric string exceeds max",
			field:   "code",
			value:   "101",
			params:  []string{"1", "100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when non-numeric string length is within range",
			field:   "name",
			value:   "hello",
			params:  []string{"3", "10"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when non-numeric string length is below min",
			field:   "name",
			value:   "hi",
			params:  []string{"3", "10"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when non-numeric string length exceeds max",
			field:   "name",
			value:   "verylongname",
			params:  []string{"3", "10"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when int is within range",
			field:   "age",
			value:   25,
			params:  []string{"18", "65"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int equals min",
			field:   "age",
			value:   18,
			params:  []string{"18", "65"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int equals max",
			field:   "age",
			value:   65,
			params:  []string{"18", "65"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when int is below min",
			field:   "age",
			value:   17,
			params:  []string{"18", "65"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when int exceeds max",
			field:   "age",
			value:   66,
			params:  []string{"18", "65"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when float64 is within range",
			field:   "price",
			value:   50.5,
			params:  []string{"10", "100"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when float64 is below min",
			field:   "price",
			value:   5.5,
			params:  []string{"10", "100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when float64 exceeds max",
			field:   "price",
			value:   100.5,
			params:  []string{"10", "100"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when interface slice length is within range",
			field:   "items",
			value:   []interface{}{"a", "b", "c"},
			params:  []string{"1", "5"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when interface slice length is below min",
			field:   "items",
			value:   []interface{}{},
			params:  []string{"1", "5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when interface slice length exceeds max",
			field:   "items",
			value:   []interface{}{"a", "b", "c", "d", "e", "f"},
			params:  []string{"1", "5"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when only one param provided",
			field:   "age",
			value:   25,
			params:  []string{"18"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "age",
			value:   25,
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when first param is not a number",
			field:   "age",
			value:   25,
			params:  []string{"abc", "65"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when second param is not a number",
			field:   "age",
			value:   25,
			params:  []string{"18", "abc"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for unsupported type",
			field:   "data",
			value:   struct{}{},
			params:  []string{"1", "10"},
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BetweenRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("BetweenRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
