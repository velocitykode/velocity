package validation

import (
	"reflect"
	"testing"
)

// equivCase is one built-in rule, the inputs it runs against, and the
// verdict expected for each input.
type equivCase struct {
	name     string
	field    string
	rule     Rule
	data     []map[string]interface{}
	wantFail []bool
}

// equivalenceCases covers every built-in rule with at least one passing and
// one failing input, so a divergence in rule name, parameter shape, or
// parameter order shows up as a different outcome.
var equivalenceCases = []equivCase{
	{name: "required", field: "f", rule: Required(),
		data: []map[string]interface{}{{"f": "x"}, {"f": ""}, {}}, wantFail: []bool{false, true, true}},
	{name: "nullable", field: "f", rule: Nullable(),
		data: []map[string]interface{}{{"f": ""}, {"f": "x"}}, wantFail: []bool{false, false}},
	{name: "filled", field: "f", rule: Filled(),
		data: []map[string]interface{}{{"f": "x"}, {"f": ""}, {}}, wantFail: []bool{false, true, false}},
	{name: "present", field: "f", rule: Present(),
		data: []map[string]interface{}{{"f": nil}, {}}, wantFail: []bool{false, true}},

	{name: "string", field: "f", rule: String(),
		data: []map[string]interface{}{{"f": "x"}, {"f": 1}}, wantFail: []bool{false, true}},
	{name: "integer", field: "f", rule: Integer(),
		data: []map[string]interface{}{{"f": "12"}, {"f": "x"}}, wantFail: []bool{false, true}},
	{name: "numeric", field: "f", rule: Numeric(),
		data: []map[string]interface{}{{"f": "1.5"}, {"f": "x"}}, wantFail: []bool{false, true}},
	{name: "boolean", field: "f", rule: Boolean(),
		data: []map[string]interface{}{{"f": "true"}, {"f": "x"}}, wantFail: []bool{false, true}},
	{name: "array", field: "f", rule: Array(),
		data: []map[string]interface{}{{"f": []interface{}{1}}, {"f": "x"}}, wantFail: []bool{false, true}},

	{name: "email", field: "f", rule: Email(),
		data: []map[string]interface{}{{"f": "a@b.com"}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "url", field: "f", rule: URL(),
		data: []map[string]interface{}{{"f": "https://example.com"}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "url_public", field: "f", rule: URLPublic(),
		data: []map[string]interface{}{{"f": "https://example.com"}, {"f": "http://127.0.0.1"}}, wantFail: []bool{false, true}},
	{name: "alpha", field: "f", rule: Alpha(),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "a1"}}, wantFail: []bool{false, true}},
	{name: "alpha_dash", field: "f", rule: AlphaDash(),
		data: []map[string]interface{}{{"f": "a-b_1"}, {"f": "a b"}}, wantFail: []bool{false, true}},
	{name: "alpha_num", field: "f", rule: AlphaNum(),
		data: []map[string]interface{}{{"f": "a1"}, {"f": "a-"}}, wantFail: []bool{false, true}},

	{name: "min", field: "f", rule: Min(3),
		data: []map[string]interface{}{{"f": "abcd"}, {"f": "ab"}}, wantFail: []bool{false, true}},
	{name: "max", field: "f", rule: Max(3),
		data: []map[string]interface{}{{"f": "ab"}, {"f": "abcd"}}, wantFail: []bool{false, true}},
	{name: "size", field: "f", rule: Size(3),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "ab"}}, wantFail: []bool{false, true}},
	{name: "between", field: "f", rule: Between(2, 4),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "a"}, {"f": "abcde"}}, wantFail: []bool{false, true, true}},

	{name: "same", field: "f", rule: Same("other"),
		data: []map[string]interface{}{{"f": "x", "other": "x"}, {"f": "x", "other": "y"}, {"f": "x"}}, wantFail: []bool{false, true, true}},
	{name: "different", field: "f", rule: Different("other"),
		data: []map[string]interface{}{{"f": "x", "other": "y"}, {"f": "x", "other": "x"}}, wantFail: []bool{false, true}},
	{name: "confirmed", field: "password", rule: Confirmed(),
		data: []map[string]interface{}{
			{"password": "s3cret", "password_confirmation": "s3cret"},
			{"password": "s3cret", "password_confirmation": "other"},
			{"password": "s3cret"},
		}, wantFail: []bool{false, true, true}},
	{name: "accepted", field: "f", rule: Accepted(),
		data: []map[string]interface{}{{"f": "yes"}, {"f": "no"}, {"f": true}, {"f": 1}}, wantFail: []bool{false, true, false, false}},

	{name: "in", field: "f", rule: In("a", "b"),
		data: []map[string]interface{}{{"f": "a"}, {"f": "c"}}, wantFail: []bool{false, true}},
	{name: "not_in", field: "f", rule: NotIn("a", "b"),
		data: []map[string]interface{}{{"f": "c"}, {"f": "a"}}, wantFail: []bool{false, true}},

	{name: "required_if", field: "f", rule: RequiredIf("other", "yes"),
		data: []map[string]interface{}{{"other": "yes"}, {"other": "no"}, {"other": "yes", "f": "x"}}, wantFail: []bool{true, false, false}},
	{name: "required_unless", field: "f", rule: RequiredUnless("other", "yes"),
		data: []map[string]interface{}{{"other": "yes"}, {"other": "no"}, {"other": "no", "f": "x"}}, wantFail: []bool{false, true, false}},
	{name: "required_with", field: "f", rule: RequiredWith("other"),
		data: []map[string]interface{}{{"other": "x"}, {}, {"other": "x", "f": "y"}}, wantFail: []bool{true, false, false}},
	{name: "required_with multi", field: "f", rule: RequiredWith("a", "b"),
		data: []map[string]interface{}{
			{"a": "x"},
			{"b": "x"},
			{"a": "x", "b": "y"},
			{"c": "x"},
			{},
			{"b": "x", "f": "y"},
		}, wantFail: []bool{true, true, true, false, false, false}},
	{name: "required_without", field: "f", rule: RequiredWithout("other"),
		data: []map[string]interface{}{{}, {"other": "x"}, {"f": "y"}}, wantFail: []bool{true, false, false}},
	{name: "required_without multi", field: "f", rule: RequiredWithout("a", "b"),
		data: []map[string]interface{}{
			{"a": "x"},
			{"b": "x"},
			{"a": "x", "b": "y"},
			{},
			{"a": "x", "f": "y"},
		}, wantFail: []bool{true, true, false, true, false}},

	{name: "date", field: "f", rule: Date(),
		data: []map[string]interface{}{{"f": "2024-01-02"}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "date_format", field: "f", rule: DateFormat("2006-01-02"),
		data: []map[string]interface{}{{"f": "2024-01-02"}, {"f": "02/01/2024"}}, wantFail: []bool{false, true}},
	{name: "timezone", field: "f", rule: Timezone(),
		data: []map[string]interface{}{{"f": "UTC"}, {"f": "Nowhere/Nothing"}}, wantFail: []bool{false, true}},

	{name: "ip", field: "f", rule: IP(),
		data: []map[string]interface{}{{"f": "10.0.0.1"}, {"f": "::1"}, {"f": "nope"}}, wantFail: []bool{false, false, true}},
	{name: "ipv4", field: "f", rule: IPv4(),
		data: []map[string]interface{}{{"f": "10.0.0.1"}, {"f": "::1"}}, wantFail: []bool{false, true}},
	{name: "ipv6", field: "f", rule: IPv6(),
		data: []map[string]interface{}{{"f": "::1"}, {"f": "10.0.0.1"}}, wantFail: []bool{false, true}},

	{name: "regex", field: "f", rule: Regex("^[a-z]+$"),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "ab1"}}, wantFail: []bool{false, true}},
	{name: "json", field: "f", rule: JSON(),
		data: []map[string]interface{}{{"f": `{"a":1}`}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "uuid", field: "f", rule: UUID(),
		data: []map[string]interface{}{{"f": "f47ac10b-58cc-4372-a567-0e02b2c3d479"}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "ulid", field: "f", rule: ULID(),
		data: []map[string]interface{}{{"f": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, {"f": "nope"}}, wantFail: []bool{false, true}},
	{name: "starts_with", field: "f", rule: StartsWith("foo", "bar"),
		data: []map[string]interface{}{{"f": "foobaz"}, {"f": "barbaz"}, {"f": "baz"}}, wantFail: []bool{false, false, true}},
	{name: "ends_with", field: "f", rule: EndsWith(".pdf", ".png"),
		data: []map[string]interface{}{{"f": "a.pdf"}, {"f": "a.png"}, {"f": "a.exe"}}, wantFail: []bool{false, false, true}},
	{name: "password", field: "f", rule: Password(),
		data: []map[string]interface{}{{"f": "Aa1!aaaa"}, {"f": "short"}}, wantFail: []bool{false, true}},
	{name: "password with length", field: "f", rule: Password().MinLength(12),
		data: []map[string]interface{}{{"f": "Aa1!aaaaaaaa"}, {"f": "Aa1!aaaa"}}, wantFail: []bool{false, true}},

	{name: "gt field", field: "f", rule: Gt("other"),
		data: []map[string]interface{}{{"f": 10, "other": 5}, {"f": 5, "other": 10}, {"f": 5}}, wantFail: []bool{false, true, true}},
	{name: "gt literal", field: "f", rule: Gt("5"),
		data: []map[string]interface{}{{"f": 10}, {"f": 1}}, wantFail: []bool{false, true}},
	{name: "gte", field: "f", rule: Gte("other"),
		data: []map[string]interface{}{{"f": 5, "other": 5}, {"f": 4, "other": 5}}, wantFail: []bool{false, true}},
	{name: "lt", field: "f", rule: Lt("other"),
		data: []map[string]interface{}{{"f": 1, "other": 5}, {"f": 5, "other": 5}}, wantFail: []bool{false, true}},
	{name: "lte", field: "f", rule: Lte("other"),
		data: []map[string]interface{}{{"f": 5, "other": 5}, {"f": 6, "other": 5}}, wantFail: []bool{false, true}},

	{name: "file", field: "f", rule: File(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}, wantFail: []bool{true, false}},
	{name: "mimes", field: "f", rule: Mimes("jpg", "png"),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}, wantFail: []bool{true, false}},
	{name: "mimes with svg opt in", field: "f", rule: Mimes("svg").AllowSVG(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}, wantFail: []bool{true, false}},
	{name: "image", field: "f", rule: Image(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}, wantFail: []bool{true, false}},
	{name: "image with svg opt in", field: "f", rule: Image().AllowSVG(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}, wantFail: []bool{true, false}},
}

// TestTypedRules_OutcomeEquivalence asserts the typed constructor path and the
// engine's own token parser reach the same verdict on the same input. The
// token expectation is built by driving the internal parser directly, never a
// public string API.
// TestTypedRules_Outcomes runs every built-in rule against passing and
// failing inputs. The expectations were carried over verbatim from the
// differential suite that ran each case through both the typed constructors
// and the retired rule-token parser and required identical verdicts, so this
// table is the frozen record of that agreement.
func TestTypedRules_Outcomes(t *testing.T) {
	for _, tc := range equivalenceCases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.data) != len(tc.wantFail) {
				t.Fatalf("case declares %d inputs and %d expectations", len(tc.data), len(tc.wantFail))
			}
			normalized, err := normalizeRuleSet(Rules{tc.field: {tc.rule}})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			for i, data := range tc.data {
				result, err := runNormalized(data, normalized, nil)
				if err != nil {
					t.Fatalf("input %d: %v", i, err)
				}
				if got := result.HasErrors(); got != tc.wantFail[i] {
					t.Errorf("input %d %#v: failed = %v, want %v (messages %#v)",
						i, data, got, tc.wantFail[i], result.Messages())
				}
			}
		})
	}
}

// TestTypedRules_EquivalenceAcrossCombinedRules checks a multi-rule field, the
// shape real rule sets take, including the nullable short-circuit.
// TestTypedRules_CombinedRuleOutcomes covers multi-rule fields, the shape
// real rule sets take, including the nullable short-circuit.
func TestTypedRules_CombinedRuleOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		rules    []Rule
		data     []map[string]interface{}
		wantFail []bool
	}{
		{
			name:     "required email",
			rules:    []Rule{Required(), Email()},
			data:     []map[string]interface{}{{"f": "a@b.com"}, {"f": "nope"}, {"f": ""}},
			wantFail: []bool{false, true, true},
		},
		{
			name:     "nullable url short circuits",
			rules:    []Rule{Nullable(), URL()},
			data:     []map[string]interface{}{{"f": ""}, {"f": "nope"}, {"f": "https://example.com"}},
			wantFail: []bool{false, true, false},
		},
		{
			name:     "required nullable contradiction",
			rules:    []Rule{Required(), Nullable()},
			data:     []map[string]interface{}{{"f": ""}, {}},
			wantFail: []bool{false, false},
		},
		{
			name:     "string min max",
			rules:    []Rule{String(), Min(2), Max(4)},
			data:     []map[string]interface{}{{"f": "abc"}, {"f": "a"}, {"f": "abcde"}, {"f": 3}},
			wantFail: []bool{false, true, true, true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeRuleSet(Rules{"f": tc.rules})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			for i, data := range tc.data {
				result, err := runNormalized(data, normalized, nil)
				if err != nil {
					t.Fatalf("input %d: %v", i, err)
				}
				if got := result.HasErrors(); got != tc.wantFail[i] {
					t.Errorf("input %d %#v: failed = %v, want %v (messages %#v)",
						i, data, got, tc.wantFail[i], result.Messages())
				}
			}
		})
	}
}

// TestRequiredWithFamily_AnyFieldSemantics pins the outcome itself, not just
// typed / token agreement: required_with fires when ANY listed field is
// present, required_without when ANY listed field is absent. The equivalence
// harness alone cannot catch a regression here, since both paths run the same
// handler.
func TestRequiredWithFamily_AnyFieldSemantics(t *testing.T) {
	tests := []struct {
		name     string
		rule     Rule
		data     map[string]interface{}
		wantFail bool
	}{
		{name: "with: first listed present", rule: RequiredWith("a", "b", "c"), data: map[string]interface{}{"a": "x"}, wantFail: true},
		{name: "with: middle listed present", rule: RequiredWith("a", "b", "c"), data: map[string]interface{}{"b": "x"}, wantFail: true},
		{name: "with: last listed present", rule: RequiredWith("a", "b", "c"), data: map[string]interface{}{"c": "x"}, wantFail: true},
		{name: "with: all listed present", rule: RequiredWith("a", "b"), data: map[string]interface{}{"a": "x", "b": "y"}, wantFail: true},
		{name: "with: none listed present", rule: RequiredWith("a", "b", "c"), data: map[string]interface{}{"d": "x"}, wantFail: false},
		{name: "with: empty data", rule: RequiredWith("a", "b"), data: map[string]interface{}{}, wantFail: false},
		{name: "with: listed present but value supplied", rule: RequiredWith("a", "b"), data: map[string]interface{}{"b": "x", "f": "given"}, wantFail: false},

		{name: "without: first listed absent", rule: RequiredWithout("a", "b", "c"), data: map[string]interface{}{"b": "x", "c": "y"}, wantFail: true},
		{name: "without: middle listed absent", rule: RequiredWithout("a", "b", "c"), data: map[string]interface{}{"a": "x", "c": "y"}, wantFail: true},
		{name: "without: last listed absent", rule: RequiredWithout("a", "b", "c"), data: map[string]interface{}{"a": "x", "b": "y"}, wantFail: true},
		{name: "without: none listed present", rule: RequiredWithout("a", "b"), data: map[string]interface{}{}, wantFail: true},
		{name: "without: all listed present", rule: RequiredWithout("a", "b"), data: map[string]interface{}{"a": "x", "b": "y"}, wantFail: false},
		{name: "without: listed absent but value supplied", rule: RequiredWithout("a", "b"), data: map[string]interface{}{"a": "x", "f": "given"}, wantFail: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeRuleSet(Rules{"f": {tc.rule}})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			result, err := runNormalized(tc.data, normalized, nil)
			if err != nil {
				t.Fatalf("runNormalized: %v", err)
			}
			if got := result.HasErrors(); got != tc.wantFail {
				t.Errorf("failed = %v, want %v (messages %#v)", got, tc.wantFail, result.Messages())
			}
		})
	}
}

// TestTypedRules_DBRuleParamsReachHandler pins the positional contract the
// unique / exists handlers read: table, column, except, id column.
func TestTypedRules_DBRuleParamsReachHandler(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		want []string
	}{
		{name: "unique", rule: Unique("users", "email"), want: []string{"users", "email"}},
		{name: "unique except", rule: Unique("users", "email").Except(7), want: []string{"users", "email", "7"}},
		{name: "unique except id column", rule: Unique("users", "email").Except("u1").IDColumn("uid"), want: []string{"users", "email", "u1", "uid"}},
		{name: "exists", rule: Exists("users", "id"), want: []string{"users", "id"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			capture := func(field string, value interface{}, params []string, data map[string]interface{}) error {
				got = append([]string(nil), params...)
				return nil
			}
			normalized, err := normalizeRuleSet(Rules{"email": {tc.rule}})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			extra := map[string]RuleHandler{tc.rule.Rule().Name: capture}
			if _, err := runNormalized(map[string]interface{}{"email": "a@b.com"}, normalized, extra); err != nil {
				t.Fatalf("runNormalized: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("params = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestTypedRules_ParametersAreNotRetokenized is the injection regression: a
// parameter carrying the token grammar's own separators must survive as one
// rule with verbatim parameters. The token form shatters on the same input,
// which is why the typed path converts specs directly.
func TestTypedRules_ParametersAreNotRetokenized(t *testing.T) {
	tests := []struct {
		name   string
		rule   Rule
		want   []string
		pass   map[string]interface{}
		reject map[string]interface{}
	}{
		{
			name:   "regex with alternation",
			rule:   Regex("^foo|bar$"),
			want:   []string{"^foo|bar$"},
			pass:   map[string]interface{}{"f": "bar"},
			reject: map[string]interface{}{"f": "baz"},
		},
		{
			name:   "regex with comma quantifier",
			rule:   Regex("^[a-z]{2,4}$"),
			want:   []string{"^[a-z]{2,4}$"},
			pass:   map[string]interface{}{"f": "abc"},
			reject: map[string]interface{}{"f": "abcdef"},
		},
		{
			name:   "in values containing commas",
			rule:   In("last, first", "first last"),
			want:   []string{"last, first", "first last"},
			pass:   map[string]interface{}{"f": "last, first"},
			reject: map[string]interface{}{"f": "last"},
		},
		{
			name:   "date format with colons",
			rule:   DateFormat("15:04:05"),
			want:   []string{"15:04:05"},
			pass:   map[string]interface{}{"f": "13:45:00"},
			reject: map[string]interface{}{"f": "13-45-00"},
		},
		{
			name:   "date format with comma",
			rule:   DateFormat("Mon, 02 Jan 2006"),
			want:   []string{"Mon, 02 Jan 2006"},
			pass:   map[string]interface{}{"f": "Tue, 02 Jan 2024"},
			reject: map[string]interface{}{"f": "2024-01-02"},
		},
		{
			name:   "starts_with prefix containing a pipe",
			rule:   StartsWith("a|b"),
			want:   []string{"a|b"},
			pass:   map[string]interface{}{"f": "a|bc"},
			reject: map[string]interface{}{"f": "abc"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeRuleSet(Rules{"f": {tc.rule}})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			parsed := normalized.fields["f"]
			if len(parsed) != 1 {
				t.Fatalf("rule count = %d, want 1 (parameters were re-tokenized)", len(parsed))
			}
			if !reflect.DeepEqual(parsed[0].params, tc.want) {
				t.Fatalf("params = %#v, want %#v", parsed[0].params, tc.want)
			}

			passing, err := runNormalized(tc.pass, normalized, nil)
			if err != nil {
				t.Fatalf("runNormalized: %v", err)
			}
			if passing.HasErrors() {
				t.Errorf("input %#v rejected: %#v", tc.pass, passing.Messages())
			}
			rejecting, err := runNormalized(tc.reject, normalized, nil)
			if err != nil {
				t.Fatalf("runNormalized: %v", err)
			}
			if !rejecting.HasErrors() {
				t.Errorf("input %#v accepted, want rejection", tc.reject)
			}
		})
	}
}
