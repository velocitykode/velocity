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

func TestFlashErrorsPayload(t *testing.T) {
	fields := map[string][]string{"email": {"required"}}
	named := &bagFailure{bag: "login", fields: fields}
	unnamed := &bagFailure{fields: fields}
	plainErr := errors.New("plain")
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "PlainMap", value: fields, want: fields},
		{name: "NamedBag", value: named, want: map[string]any{FlashErrorBagKey: "login", FlashBaggedErrorsKey: fields}},
		{name: "WrappedNamedBag", value: fmt.Errorf("store: %w", named), want: map[string]any{FlashErrorBagKey: "login", FlashBaggedErrorsKey: fields}},
		{name: "EmptyBag", value: unnamed, want: unnamed},
		{name: "ErrorWithoutBag", value: plainErr, want: plainErr},
		{name: "BagWithoutFieldMessages", value: bagOnly{"name": "required"}, want: map[string]any{FlashErrorBagKey: "profile", FlashBaggedErrorsKey: bagOnly{"name": "required"}}},
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
