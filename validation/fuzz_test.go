package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// FuzzNormalizeRuleSet feeds arbitrary rule names and parameters through the
// single normalization seam every public entry point goes through. Rule
// values are adopter-authored, and a custom implementation of
// contract.ValidationRule can hand the engine anything.
//
// Contract:
//  1. Never panic.
//  2. Either report an error or produce exactly one parsed rule per input
//     rule, in order, with the name and parameters carried verbatim.
//  3. An empty name is always an error, never a rule the engine would then
//     fail to resolve at evaluation time.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzNormalizeRuleSet -fuzztime=30s ./validation
func FuzzNormalizeRuleSet(f *testing.F) {
	seeds := []struct {
		name  string
		param string
	}{
		{"", ""},
		{"required", ""},
		{"min", "3"},
		{"in", "a,b,c"},
		{"regex", "^foo|bar$"},
		{"date_format", "Mon, 02 Jan 2006"},
		{"required", strings.Repeat("x", 1000)},
		{"\x00", "\x01"},
		{" ", " "},
		{":", ":"},
		{"|", "|"},
		{"unique", "users,email"},
	}
	for _, s := range seeds {
		f.Add(s.name, s.param)
	}

	f.Fuzz(func(t *testing.T, name, param string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic normalizing name=%q param=%q: %v", name, param, r)
			}
		}()

		rule := staticRule{spec: contract.ValidationRuleSpec{Name: name, Params: []string{param}}}
		normalized, err := normalizeRuleSet(Rules{"f": {rule}})
		if err != nil {
			if !errors.Is(err, ErrInvalidRule) {
				t.Errorf("error does not wrap ErrInvalidRule: %v", err)
			}
			if name != "" {
				t.Errorf("normalizeRuleSet(name=%q) reported %v; only an empty name is malformed here", name, err)
			}
			return
		}

		if name == "" {
			t.Fatalf("empty rule name was accepted")
		}
		got := normalized.fields["f"]
		if len(got) != 1 {
			t.Fatalf("rule count = %d, want 1", len(got))
		}
		if got[0].name != name {
			t.Errorf("name = %q, want %q", got[0].name, name)
		}
		if len(got[0].params) != 1 || got[0].params[0] != param {
			t.Errorf("params = %#v, want [%q]", got[0].params, param)
		}
	})
}
