package router

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// bagFailure names its error bag and carries field messages.
type bagFailure struct {
	bag    string
	fields map[string][]string
}

func (f *bagFailure) Error() string               { return "invalid" }
func (f *bagFailure) ErrorBag() string            { return f.bag }
func (f *bagFailure) Errors() map[string][]string { return f.fields }

// bagOnly names its error bag without field messages.
type bagOnly map[string]string

func (bagOnly) ErrorBag() string { return "profile" }

// fieldsOnly carries field messages without naming a bag and is not an
// error.
type fieldsOnly map[string][]string

func (f fieldsOnly) Errors() map[string][]string { return f }

func TestFlashErrorsPayload(t *testing.T) {
	fields := map[string][]string{"email": {"required", "email"}, "name": {}}
	first := map[string]string{"email": "required"}
	named := &bagFailure{bag: "login", fields: fields}
	unnamed := &bagFailure{fields: fields}
	plainErr := errors.New("plain")
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "PlainMap", value: fields, want: first},
		{name: "PlainStringMap", value: map[string]string{"email": "required"}, want: map[string]string{"email": "required"}},
		{name: "AnyMapWithLists", value: map[string]any{"email": []string{"required", "email"}, "name": []any{"short"}, "age": []any{}, "code": "bad"}, want: map[string]any{"email": "required", "name": "short", "code": "bad"}},
		{name: "NamedBag", value: named, want: map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: first}},
		{name: "WrappedNamedBag", value: fmt.Errorf("store: %w", named), want: map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: first}},
		{name: "EmptyBag", value: unnamed, want: first},
		{name: "WrappedEmptyBag", value: fmt.Errorf("store: %w", unnamed), want: first},
		{name: "FieldsWithoutBag", value: fieldsOnly(fields), want: first},
		{name: "ErrorWithoutBag", value: plainErr, want: plainErr},
		{name: "BagWithoutFieldMessages", value: bagOnly{"name": "required"}, want: map[string]any{flashErrorBagKey: "profile", flashBaggedErrorsKey: bagOnly{"name": "required"}}},
		{name: "Nil", value: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flashErrorsPayload(tt.value); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("flashErrorsPayload = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestUnwrapErrorBag asserts the page view of an opened errors value: an
// error bag envelope exposes its fields at the top level and under the
// bag's name; anything else is returned unchanged.
func TestUnwrapErrorBag(t *testing.T) {
	fields := map[string]any{"email": "required"}
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "PlainErrors", value: fields, want: fields},
		{
			name:  "BaggedErrors",
			value: map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: fields},
			want:  map[string]any{"email": "required", "login": fields},
		},
		{
			name:  "EmptyBag",
			value: map[string]any{flashErrorBagKey: "", flashBaggedErrorsKey: fields},
			want:  map[string]any{flashErrorBagKey: "", flashBaggedErrorsKey: fields},
		},
		{
			name:  "NonStringBag",
			value: map[string]any{flashErrorBagKey: 3.0, flashBaggedErrorsKey: fields},
			want:  map[string]any{flashErrorBagKey: 3.0, flashBaggedErrorsKey: fields},
		},
		{
			name:  "NonObjectErrors",
			value: map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: "oops"},
			want:  map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: "oops"},
		},
		{
			name:  "ExtraMember",
			value: map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: fields, "x": 1.0},
			want:  map[string]any{flashErrorBagKey: "login", flashBaggedErrorsKey: fields, "x": 1.0},
		},
		{name: "StringValue", value: "error", want: "error"},
		{name: "NilValue", value: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unwrapErrorBag(tt.value); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unwrapErrorBag = %#v, want %#v", got, tt.want)
			}
		})
	}
}
