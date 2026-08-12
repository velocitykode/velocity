package validation

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// staticRule returns a caller-supplied spec, so tests can build rule values
// the constructors would never produce (empty name, stray handler).
type staticRule struct {
	spec contract.ValidationRuleSpec
}

func (s staticRule) Rule() contract.ValidationRuleSpec { return s.spec }

// nilableRule has a pointer receiver so a nil *nilableRule can be stored in a
// contract.ValidationRule interface as a typed nil.
type nilableRule struct{}

func (n *nilableRule) Rule() contract.ValidationRuleSpec {
	return contract.ValidationRuleSpec{Name: "nilable"}
}

func noopHandler(field string, value interface{}, params []string, data map[string]interface{}) error {
	return nil
}

// sharedEvenRule is one rule value reused across fields, the supported way to
// apply a custom rule to more than one field.
var sharedEvenRule = Custom("even", noopHandler)

func TestNormalizeRuleSet_Errors(t *testing.T) {
	var typedNil *nilableRule

	tests := []struct {
		name  string
		rules Rules
		want  string
	}{
		{
			name:  "nil rule",
			rules: Rules{"f": {nil}},
			want:  `field "f" rule 0 is nil`,
		},
		{
			name:  "nil rule after a valid one",
			rules: Rules{"f": {Required(), nil}},
			want:  `field "f" rule 1 is nil`,
		},
		{
			name:  "typed nil rule",
			rules: Rules{"f": {typedNil}},
			want:  `field "f" rule 0 is nil`,
		},
		{
			name:  "empty rule name",
			rules: Rules{"f": {staticRule{}}},
			want:  `field "f" rule 0 has an empty name`,
		},
		{
			name:  "unique except of an unusable type",
			rules: Rules{"f": {Unique("users", "email").Except(3.5)}},
			want:  "must be an integer, string, or fmt.Stringer",
		},
		{
			name:  "unique except of a nil id",
			rules: Rules{"f": {Unique("users", "email").Except(nil)}},
			want:  "must be an integer, string, or fmt.Stringer",
		},
		{
			name:  "custom rule with a nil handler",
			rules: Rules{"f": {Custom("even", nil)}},
			want:  `custom rule "even" on field "f" has a nil handler`,
		},
		{
			name:  "custom rule shadowing a built-in",
			rules: Rules{"f": {Custom("required", noopHandler)}},
			want:  `custom rule "required" on field "f" shadows a built-in rule`,
		},
		{
			name:  "custom rule shadowing a db rule",
			rules: Rules{"f": {Custom("unique", noopHandler)}},
			want:  `custom rule "unique" on field "f" shadows a built-in rule`,
		},
		{
			name: "same custom name from two rule values",
			rules: Rules{
				"a": {Custom("even", noopHandler)},
				"b": {Custom("even", noopHandler)},
			},
			want: `custom rule "even" is declared by two different rule values`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeRuleSet(tc.rules)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrInvalidRule) {
				t.Errorf("error does not wrap ErrInvalidRule: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestNormalizeRuleSet_Accepts(t *testing.T) {
	tests := []struct {
		name       string
		rules      Rules
		wantFields map[string]int
		wantCustom []string
	}{
		{
			name:       "nil rule set",
			rules:      nil,
			wantFields: map[string]int{},
		},
		{
			name:       "empty rule slice",
			rules:      Rules{"f": {}},
			wantFields: map[string]int{"f": 0},
		},
		{
			name:       "built-ins",
			rules:      Rules{"email": {Required(), Email()}, "age": {Integer(), Min(18)}},
			wantFields: map[string]int{"email": 2, "age": 2},
		},
		{
			name:       "one custom rule value reused across fields",
			rules:      Rules{"a": {sharedEvenRule}, "b": {sharedEvenRule}},
			wantFields: map[string]int{"a": 1, "b": 1},
			wantCustom: []string{"even"},
		},
		{
			name:       "unique with every builder",
			rules:      Rules{"email": {Unique("users", "email").Except(7).IDColumn("uid")}},
			wantFields: map[string]int{"email": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeRuleSet(tc.rules)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.fields) != len(tc.wantFields) {
				t.Fatalf("field count = %d, want %d", len(got.fields), len(tc.wantFields))
			}
			for field, count := range tc.wantFields {
				if len(got.fields[field]) != count {
					t.Errorf("field %q rule count = %d, want %d", field, len(got.fields[field]), count)
				}
			}
			if len(got.custom) != len(tc.wantCustom) {
				t.Fatalf("custom count = %d, want %d", len(got.custom), len(tc.wantCustom))
			}
			for _, name := range tc.wantCustom {
				if _, ok := got.custom[name]; !ok {
					t.Errorf("custom handler %q was not carried", name)
				}
			}
		})
	}
}

// TestNormalizeRuleSet_CarriesHandlerWithoutMarker covers an adopter-defined
// rule value that supplies a handler without implementing the custom marker:
// it is carried, and still cannot shadow a built-in.
func TestNormalizeRuleSet_CarriesHandlerWithoutMarker(t *testing.T) {
	carried := staticRule{spec: contract.ValidationRuleSpec{Name: "adopter", Handler: noopHandler}}
	got, err := normalizeRuleSet(Rules{"f": {carried}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.custom["adopter"]; !ok {
		t.Error("handler was not carried")
	}

	shadowing := staticRule{spec: contract.ValidationRuleSpec{Name: "email", Handler: noopHandler}}
	if _, err := normalizeRuleSet(Rules{"f": {shadowing}}); err == nil {
		t.Error("expected a shadowing error")
	}

	// A spec with no handler and no marker is left for the registry to
	// resolve at evaluation time.
	plain := staticRule{spec: contract.ValidationRuleSpec{Name: "registered_elsewhere"}}
	if _, err := normalizeRuleSet(Rules{"f": {plain}}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNormalizeRuleSet_PreservesRuleOrder(t *testing.T) {
	got, err := normalizeRuleSet(Rules{"f": {Required(), Min(2), Max(4), Email()}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"required", "min", "max", "email"}
	if len(got.fields["f"]) != len(want) {
		t.Fatalf("rule count = %d, want %d", len(got.fields["f"]), len(want))
	}
	for i, name := range want {
		if got.fields["f"][i].name != name {
			t.Errorf("rule %d = %q, want %q", i, got.fields["f"][i].name, name)
		}
	}
}

// TestRunNormalized_CustomRuleRunsOnFreshValidator covers the path that the
// registry-only design could not reach: a custom rule that works without
// prior registration on a long-lived validator.
func TestRunNormalized_CustomRuleRunsOnFreshValidator(t *testing.T) {
	even := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		n, ok := value.(int)
		if !ok || n%2 != 0 {
			return fmt.Errorf("The %s field must be even.", field)
		}
		return nil
	}

	normalized, err := normalizeRuleSet(Rules{"count": {Custom("even", even)}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}

	passing, err := runNormalized(map[string]interface{}{"count": 4}, normalized, nil)
	if err != nil {
		t.Fatalf("runNormalized: %v", err)
	}
	if passing.HasErrors() {
		t.Errorf("even value rejected: %#v", passing.Messages())
	}

	failing, err := runNormalized(map[string]interface{}{"count": 3}, normalized, nil)
	if err != nil {
		t.Fatalf("runNormalized: %v", err)
	}
	if got := failing.First("count"); got != "The count field must be even." {
		t.Errorf("message = %q, want the custom rule message", got)
	}
}

// TestRunNormalized_CustomRuleCollidingWithExtra reports rather than panics
// when a carried handler cannot be registered alongside the DB handlers.
// TestRunNormalized_UnresolvableRuleIsAConfigError covers the Check path: an
// unregistered rule name is returned as an error, never as a field failure.
func TestRunNormalized_UnresolvableRuleIsAConfigError(t *testing.T) {
	typo := staticRule{spec: contract.ValidationRuleSpec{Name: "uniqe", Params: []string{"users", "email"}}}
	normalized, err := normalizeRuleSet(Rules{"email": {typo}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}

	result, err := runNormalized(map[string]interface{}{"email": "a@b.com"}, normalized, nil)
	if err == nil {
		t.Fatal("expected an error for an unregistered rule name")
	}
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
	if result != nil {
		t.Error("no result should be produced when the set cannot run")
	}

	// The same name resolves once a handler is supplied as an extra, which
	// is how the DB-backed rules reach the engine.
	extra := map[string]RuleHandler{"uniqe": noopHandler}
	if _, err := runNormalized(map[string]interface{}{"email": "a@b.com"}, normalized, extra); err != nil {
		t.Errorf("unexpected error once the handler is installed: %v", err)
	}
}

// TestCheckData_UnresolvableRuleIsAConfigError covers the same through the
// public entry point.
func TestCheckData_UnresolvableRuleIsAConfigError(t *testing.T) {
	typo := staticRule{spec: contract.ValidationRuleSpec{Name: "requried"}}

	result, err := CheckData(map[string]interface{}{"email": ""}, Rules{"email": {typo}})
	if err == nil {
		t.Fatal("expected an error for an unregistered rule name")
	}
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
	if result != nil {
		t.Error("no result should be produced when the set cannot run")
	}
}

// TestCheckData_UniqueWithoutDatabase reports the missing handler rather than
// failing the field: naming a DB rule with no database wired is a wiring bug.
func TestCheckData_UniqueWithoutDatabase(t *testing.T) {
	_, err := CheckData(map[string]interface{}{"email": "a@b.com"}, Rules{
		"email": {Required(), Unique("users", "email")},
	})
	if err == nil {
		t.Fatal("expected an error for a DB rule with no handler installed")
	}
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
	if !strings.Contains(err.Error(), `names rule "unique"`) {
		t.Errorf("error = %q, want it to name the unique rule", err.Error())
	}
}

func TestRunNormalized_CustomRuleCollidingWithExtra(t *testing.T) {
	normalized, err := normalizeRuleSet(Rules{"f": {Custom("lookup", noopHandler)}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	extra := map[string]RuleHandler{"lookup": noopHandler}

	result, err := runNormalized(map[string]interface{}{"f": "x"}, normalized, extra)
	if err == nil {
		t.Fatal("expected a registration error")
	}
	if result != nil {
		t.Error("no result should be returned when the set cannot run")
	}
	if !errors.Is(err, ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
}

func TestRunNormalized_ExtraHandlerRejected(t *testing.T) {
	normalized, err := normalizeRuleSet(Rules{"f": {Required()}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	if _, err := runNormalized(map[string]interface{}{"f": "x"}, normalized, map[string]RuleHandler{"required": noopHandler}); err == nil {
		t.Error("expected an error for an extra handler shadowing a built-in")
	}
	if _, err := runNormalized(map[string]interface{}{"f": "x"}, normalized, map[string]RuleHandler{"nilhandler": nil}); err == nil {
		t.Error("expected an error for a nil extra handler")
	}
}

func TestRunNormalized_AppliesMessageOverrides(t *testing.T) {
	normalized, err := normalizeRuleSet(Rules{"email": {Required()}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	result, err := runNormalized(map[string]interface{}{"email": ""}, normalized, nil, Messages{{Field: "email", Rule: "required"}: "We need your email."})
	if err != nil {
		t.Fatalf("runNormalized: %v", err)
	}
	if got := result.First("email"); got != "We need your email." {
		t.Errorf("message = %q, want the override", got)
	}
}

func TestValidateNormalized_RejectsUnsupportedData(t *testing.T) {
	normalized, err := normalizeRuleSet(Rules{"f": {Required()}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	if _, err := newDefaultValidator().validateNormalized([]string{"not a map"}, normalized); err == nil {
		t.Error("expected an error for unsupported input data")
	}
}

func TestValidateNormalized_ReportsValidatedData(t *testing.T) {
	normalized, err := normalizeRuleSet(Rules{"email": {Required(), Email()}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	validated, err := newDefaultValidator().validateNormalized(map[string]interface{}{"email": "a@b.com"}, normalized)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := validated.GetString("email"); got != "a@b.com" {
		t.Errorf("validated email = %q, want a@b.com", got)
	}

	validated, err = newDefaultValidator().validateNormalized(map[string]interface{}{"email": "nope"}, normalized)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !validated.HasErrors() {
		t.Error("validated data should carry the field errors")
	}
}

// sharedRules is a package-level rule set, the shape adopters are expected to
// use. Rule values must be immutable so this can be normalized and evaluated
// concurrently.
var sharedRules = Rules{
	"email":    {Required(), Email()},
	"password": {Required(), Min(8), Confirmed()},
	"role":     {In("admin", "user")},
	"website":  {Nullable(), URL()},
	"slug":     {Regex("^[a-z0-9|-]+$")},
	"upserted": {Unique("users", "email").Except(7)},
}

func TestSharedRuleSet_ConcurrentReuse(t *testing.T) {
	const goroutines = 16
	const iterations = 40

	inputs := []map[string]interface{}{
		{"email": "a@b.com", "password": "s3cret!!", "password_confirmation": "s3cret!!", "role": "admin", "website": "", "slug": "a-b"},
		{"email": "nope", "password": "short", "role": "root", "website": "not a url", "slug": "NOPE"},
	}

	unique := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) != 3 || params[2] != "7" {
			return fmt.Errorf("unexpected params %#v", params)
		}
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*iterations)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				normalized, err := normalizeRuleSet(sharedRules)
				if err != nil {
					errCh <- err
					return
				}
				extra := map[string]RuleHandler{"unique": unique}
				if _, err := runNormalized(inputs[(g+i)%len(inputs)], normalized, extra); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent run failed: %v", err)
	}

	// The shared rules must be unchanged after concurrent use.
	if got := sharedRules["upserted"][0].Rule().Params; len(got) != 3 || got[0] != "users" || got[2] != "7" {
		t.Errorf("shared rule mutated: %#v", got)
	}
	if got := sharedRules["role"][0].Rule().Params; len(got) != 2 || got[0] != "admin" {
		t.Errorf("shared rule mutated: %#v", got)
	}
}

// TestSharedRuleSet_ParamsNotMutatedByHandlers guards the defensive copy in
// validateField: a handler that writes to its params must not corrupt the
// shared rule value.
func TestSharedRuleSet_ParamsNotMutatedByHandlers(t *testing.T) {
	rules := Rules{"f": {Custom("scribble", func(field string, value interface{}, params []string, data map[string]interface{}) error {
		for i := range params {
			params[i] = "clobbered"
		}
		return nil
	})}}
	// The custom rule carries no params, so pair it with a built-in whose
	// params the handler could reach.
	rules["g"] = []Rule{In("a", "b")}

	normalized, err := normalizeRuleSet(rules)
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := runNormalized(map[string]interface{}{"f": "x", "g": "a"}, normalized, nil); err != nil {
			t.Fatalf("runNormalized: %v", err)
		}
	}
	if got := rules["g"][0].Rule().Params; got[0] != "a" || got[1] != "b" {
		t.Errorf("rule params were mutated: %#v", got)
	}
}

func TestIsNilRule(t *testing.T) {
	var typedNil *nilableRule
	var nilSet Rules

	tests := []struct {
		name string
		rule contract.ValidationRule
		want bool
	}{
		{name: "nil interface", rule: nil, want: true},
		{name: "typed nil pointer", rule: typedNil, want: true},
		{name: "value rule", rule: Required(), want: false},
		{name: "non-nil pointer", rule: &nilableRule{}, want: false},
		{name: "nil map kind", rule: nilRuleMap(nilSet), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNilRule(tc.rule); got != tc.want {
				t.Errorf("isNilRule = %v, want %v", got, tc.want)
			}
		})
	}
}

// nilRuleMap is a map-kinded rule implementation, used to cover the non-pointer
// nil kinds isNilRule guards.
type nilRuleMap Rules

func (n nilRuleMap) Rule() contract.ValidationRuleSpec {
	return contract.ValidationRuleSpec{Name: "map"}
}

// TestHandlerCarrierMarker pins the contract normalization relies on to tell
// a custom rule missing its handler from a built-in, which has none.
func TestHandlerCarrierMarker(t *testing.T) {
	hc, ok := Custom("x", noopHandler).(handlerCarrier)
	if !ok {
		t.Fatal("Custom must mark itself as carrying a handler")
	}
	hc.carriesHandler()

	if _, ok := Required().(handlerCarrier); ok {
		t.Error("built-in rules must not be marked as handler carriers")
	}
}

// TestCarriedRule_DuplicateNames pins the identity rule: one Custom value
// may appear on many fields, two distinct values claiming one name may not.
// Go cannot compare handlers, so a second declaration is refused rather than
// resolved by whichever the map happens to yield first.
func TestCarriedRule_DuplicateNames(t *testing.T) {
	// Two rules built from the SAME closure literal with DIFFERENT captures:
	// their code pointers are identical, their behaviour is not.
	digits := func(want int) RuleHandler {
		return func(field string, value interface{}, params []string, data map[string]interface{}) error {
			s, _ := value.(string)
			if len(s) != want {
				return fmt.Errorf("The %s field must have %d digits.", field, want)
			}
			return nil
		}
	}

	shared := Custom("phone", digits(7))

	t.Run("same value on two fields", func(t *testing.T) {
		got, err := normalizeRuleSet(Rules{"home": {shared}, "work": {shared}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got.custom) != 1 {
			t.Errorf("carried count = %d, want 1", len(got.custom))
		}
	})

	t.Run("two values from one closure literal", func(t *testing.T) {
		rules := Rules{"home": {Custom("phone", digits(7))}, "work": {Custom("phone", digits(10))}}
		// Map traversal order varies per run; the verdict must not.
		for i := 0; i < 50; i++ {
			_, err := normalizeRuleSet(rules)
			if err == nil {
				t.Fatalf("iteration %d: expected an error", i)
			}
			if !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("iteration %d: error does not wrap ErrInvalidRule: %v", i, err)
			}
			if !strings.Contains(err.Error(), "two different rule values") {
				t.Fatalf("iteration %d: error = %q", i, err.Error())
			}
		}
	})

	t.Run("two values on one field", func(t *testing.T) {
		if _, err := normalizeRuleSet(Rules{"home": {Custom("phone", digits(7)), Custom("phone", digits(7))}}); err == nil {
			t.Error("expected an error for two rule values sharing a name")
		}
	})
}

func TestSameRuleValue(t *testing.T) {
	shared := Custom("x", noopHandler)
	other := Custom("x", noopHandler)

	if !sameRuleValue(shared, shared) {
		t.Error("one rule value must equal itself")
	}
	if sameRuleValue(shared, other) {
		t.Error("two separately built rule values must not compare equal")
	}
	// A rule value Go cannot compare (its spec holds a slice and a func)
	// reports false instead of panicking on ==.
	uncomparable := staticRule{spec: contract.ValidationRuleSpec{Name: "x", Params: []string{"p"}, Handler: noopHandler}}
	if sameRuleValue(uncomparable, uncomparable) {
		t.Error("an uncomparable rule value must not claim identity")
	}
	if sameRuleValue(shared, uncomparable) {
		t.Error("rule values of different types must not compare equal")
	}
}

func TestRuleRegistry_RegisterErrors(t *testing.T) {
	reg := &ruleRegistry{rules: make(map[string]RuleHandler)}

	if err := reg.register("x", noopHandler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Registration reports; only the built-in bootstrap is allowed to treat
	// a failure as fatal.
	err := reg.register("x", noopHandler)
	if err == nil {
		t.Fatal("expected a duplicate-registration error")
	}
	if _, ok := err.(*contract.RegistrationError); !ok {
		t.Errorf("error = %T, want *contract.RegistrationError", err)
	}
	if err := reg.register("y", nil); err == nil {
		t.Error("expected a nil-handler error")
	}
}
