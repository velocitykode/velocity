package validation

import (
	"reflect"
	"testing"
)

// equivCase pairs a typed rule with the token form the engine's parser
// produced for the same rule, plus the inputs both are run against.
type equivCase struct {
	name  string
	field string
	token string
	rule  Rule
	data  []map[string]interface{}
}

// equivalenceCases covers every built-in rule with at least one passing and
// one failing input, so a divergence in rule name, parameter shape, or
// parameter order shows up as a different outcome.
var equivalenceCases = []equivCase{
	{name: "required", field: "f", token: "required", rule: Required(),
		data: []map[string]interface{}{{"f": "x"}, {"f": ""}, {}}},
	{name: "nullable", field: "f", token: "nullable", rule: Nullable(),
		data: []map[string]interface{}{{"f": ""}, {"f": "x"}}},
	{name: "filled", field: "f", token: "filled", rule: Filled(),
		data: []map[string]interface{}{{"f": "x"}, {"f": ""}, {}}},
	{name: "present", field: "f", token: "present", rule: Present(),
		data: []map[string]interface{}{{"f": nil}, {}}},

	{name: "string", field: "f", token: "string", rule: String(),
		data: []map[string]interface{}{{"f": "x"}, {"f": 1}}},
	{name: "integer", field: "f", token: "integer", rule: Integer(),
		data: []map[string]interface{}{{"f": "12"}, {"f": "x"}}},
	{name: "numeric", field: "f", token: "numeric", rule: Numeric(),
		data: []map[string]interface{}{{"f": "1.5"}, {"f": "x"}}},
	{name: "boolean", field: "f", token: "boolean", rule: Boolean(),
		data: []map[string]interface{}{{"f": "true"}, {"f": "x"}}},
	{name: "array", field: "f", token: "array", rule: Array(),
		data: []map[string]interface{}{{"f": []interface{}{1}}, {"f": "x"}}},

	{name: "email", field: "f", token: "email", rule: Email(),
		data: []map[string]interface{}{{"f": "a@b.com"}, {"f": "nope"}}},
	{name: "url", field: "f", token: "url", rule: URL(),
		data: []map[string]interface{}{{"f": "https://example.com"}, {"f": "nope"}}},
	{name: "url_public", field: "f", token: "url_public", rule: URLPublic(),
		data: []map[string]interface{}{{"f": "https://example.com"}, {"f": "http://127.0.0.1"}}},
	{name: "alpha", field: "f", token: "alpha", rule: Alpha(),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "a1"}}},
	{name: "alpha_dash", field: "f", token: "alpha_dash", rule: AlphaDash(),
		data: []map[string]interface{}{{"f": "a-b_1"}, {"f": "a b"}}},
	{name: "alpha_num", field: "f", token: "alpha_num", rule: AlphaNum(),
		data: []map[string]interface{}{{"f": "a1"}, {"f": "a-"}}},

	{name: "min", field: "f", token: "min:3", rule: Min(3),
		data: []map[string]interface{}{{"f": "abcd"}, {"f": "ab"}}},
	{name: "max", field: "f", token: "max:3", rule: Max(3),
		data: []map[string]interface{}{{"f": "ab"}, {"f": "abcd"}}},
	{name: "size", field: "f", token: "size:3", rule: Size(3),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "ab"}}},
	{name: "between", field: "f", token: "between:2,4", rule: Between(2, 4),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "a"}, {"f": "abcde"}}},

	{name: "same", field: "f", token: "same:other", rule: Same("other"),
		data: []map[string]interface{}{{"f": "x", "other": "x"}, {"f": "x", "other": "y"}, {"f": "x"}}},
	{name: "different", field: "f", token: "different:other", rule: Different("other"),
		data: []map[string]interface{}{{"f": "x", "other": "y"}, {"f": "x", "other": "x"}}},
	{name: "confirmed", field: "password", token: "confirmed", rule: Confirmed(),
		data: []map[string]interface{}{
			{"password": "s3cret", "password_confirmation": "s3cret"},
			{"password": "s3cret", "password_confirmation": "other"},
			{"password": "s3cret"},
		}},
	{name: "accepted", field: "f", token: "accepted", rule: Accepted(),
		data: []map[string]interface{}{{"f": "yes"}, {"f": "no"}, {"f": true}, {"f": 1}}},

	{name: "in", field: "f", token: "in:a,b", rule: In("a", "b"),
		data: []map[string]interface{}{{"f": "a"}, {"f": "c"}}},
	{name: "not_in", field: "f", token: "not_in:a,b", rule: NotIn("a", "b"),
		data: []map[string]interface{}{{"f": "c"}, {"f": "a"}}},

	{name: "required_if", field: "f", token: "required_if:other,yes", rule: RequiredIf("other", "yes"),
		data: []map[string]interface{}{{"other": "yes"}, {"other": "no"}, {"other": "yes", "f": "x"}}},
	{name: "required_unless", field: "f", token: "required_unless:other,yes", rule: RequiredUnless("other", "yes"),
		data: []map[string]interface{}{{"other": "yes"}, {"other": "no"}, {"other": "no", "f": "x"}}},
	{name: "required_with", field: "f", token: "required_with:other", rule: RequiredWith("other"),
		data: []map[string]interface{}{{"other": "x"}, {}, {"other": "x", "f": "y"}}},
	{name: "required_with multi", field: "f", token: "required_with:a,b", rule: RequiredWith("a", "b"),
		data: []map[string]interface{}{
			{"a": "x"},
			{"b": "x"},
			{"a": "x", "b": "y"},
			{"c": "x"},
			{},
			{"b": "x", "f": "y"},
		}},
	{name: "required_without", field: "f", token: "required_without:other", rule: RequiredWithout("other"),
		data: []map[string]interface{}{{}, {"other": "x"}, {"f": "y"}}},
	{name: "required_without multi", field: "f", token: "required_without:a,b", rule: RequiredWithout("a", "b"),
		data: []map[string]interface{}{
			{"a": "x"},
			{"b": "x"},
			{"a": "x", "b": "y"},
			{},
			{"a": "x", "f": "y"},
		}},

	{name: "date", field: "f", token: "date", rule: Date(),
		data: []map[string]interface{}{{"f": "2024-01-02"}, {"f": "nope"}}},
	{name: "date_format", field: "f", token: "date_format:2006-01-02", rule: DateFormat("2006-01-02"),
		data: []map[string]interface{}{{"f": "2024-01-02"}, {"f": "02/01/2024"}}},
	{name: "timezone", field: "f", token: "timezone", rule: Timezone(),
		data: []map[string]interface{}{{"f": "UTC"}, {"f": "Nowhere/Nothing"}}},

	{name: "ip", field: "f", token: "ip", rule: IP(),
		data: []map[string]interface{}{{"f": "10.0.0.1"}, {"f": "::1"}, {"f": "nope"}}},
	{name: "ipv4", field: "f", token: "ipv4", rule: IPv4(),
		data: []map[string]interface{}{{"f": "10.0.0.1"}, {"f": "::1"}}},
	{name: "ipv6", field: "f", token: "ipv6", rule: IPv6(),
		data: []map[string]interface{}{{"f": "::1"}, {"f": "10.0.0.1"}}},

	{name: "regex", field: "f", token: "regex:^[a-z]+$", rule: Regex("^[a-z]+$"),
		data: []map[string]interface{}{{"f": "abc"}, {"f": "ab1"}}},
	{name: "json", field: "f", token: "json", rule: JSON(),
		data: []map[string]interface{}{{"f": `{"a":1}`}, {"f": "nope"}}},
	{name: "uuid", field: "f", token: "uuid", rule: UUID(),
		data: []map[string]interface{}{{"f": "f47ac10b-58cc-4372-a567-0e02b2c3d479"}, {"f": "nope"}}},
	{name: "ulid", field: "f", token: "ulid", rule: ULID(),
		data: []map[string]interface{}{{"f": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, {"f": "nope"}}},
	{name: "starts_with", field: "f", token: "starts_with:foo,bar", rule: StartsWith("foo", "bar"),
		data: []map[string]interface{}{{"f": "foobaz"}, {"f": "barbaz"}, {"f": "baz"}}},
	{name: "ends_with", field: "f", token: "ends_with:.pdf,.png", rule: EndsWith(".pdf", ".png"),
		data: []map[string]interface{}{{"f": "a.pdf"}, {"f": "a.png"}, {"f": "a.exe"}}},
	{name: "password", field: "f", token: "password", rule: Password(),
		data: []map[string]interface{}{{"f": "Aa1!aaaa"}, {"f": "short"}}},
	{name: "password with length", field: "f", token: "password:12", rule: Password().MinLength(12),
		data: []map[string]interface{}{{"f": "Aa1!aaaaaaaa"}, {"f": "Aa1!aaaa"}}},

	{name: "gt field", field: "f", token: "gt:other", rule: Gt("other"),
		data: []map[string]interface{}{{"f": 10, "other": 5}, {"f": 5, "other": 10}, {"f": 5}}},
	{name: "gt literal", field: "f", token: "gt:5", rule: Gt("5"),
		data: []map[string]interface{}{{"f": 10}, {"f": 1}}},
	{name: "gte", field: "f", token: "gte:other", rule: Gte("other"),
		data: []map[string]interface{}{{"f": 5, "other": 5}, {"f": 4, "other": 5}}},
	{name: "lt", field: "f", token: "lt:other", rule: Lt("other"),
		data: []map[string]interface{}{{"f": 1, "other": 5}, {"f": 5, "other": 5}}},
	{name: "lte", field: "f", token: "lte:other", rule: Lte("other"),
		data: []map[string]interface{}{{"f": 5, "other": 5}, {"f": 6, "other": 5}}},

	{name: "file", field: "f", token: "file", rule: File(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}},
	{name: "mimes", field: "f", token: "mimes:jpg,png", rule: Mimes("jpg", "png"),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}},
	{name: "mimes with svg opt in", field: "f", token: "mimes:svg,allow_svg", rule: Mimes("svg").AllowSVG(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}},
	{name: "image", field: "f", token: "image", rule: Image(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}},
	{name: "image with svg opt in", field: "f", token: "image:allow_svg", rule: Image().AllowSVG(),
		data: []map[string]interface{}{{"f": "not-a-file"}, {"f": nil}}},
}

// TestTypedRules_OutcomeEquivalence asserts the typed constructor path and the
// engine's own token parser reach the same verdict on the same input. The
// token expectation is built by driving the internal parser directly, never a
// public string API.
func TestTypedRules_OutcomeEquivalence(t *testing.T) {
	for _, tc := range equivalenceCases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeRuleSet(RuleSet{tc.field: {tc.rule}})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			legacy := normalizedRuleSet{
				fields: map[string][]parsedRule{tc.field: parseRuleSlice([]string{tc.token})},
			}

			if !reflect.DeepEqual(normalized.fields, legacy.fields) {
				t.Errorf("parsed rules differ:\n typed  = %#v\n tokens = %#v", normalized.fields, legacy.fields)
			}

			for i, data := range tc.data {
				typedResult, err := runNormalized(data, normalized, nil)
				if err != nil {
					t.Fatalf("input %d: typed run: %v", i, err)
				}
				legacyResult, err := runNormalized(data, legacy, nil)
				if err != nil {
					t.Fatalf("input %d: token run: %v", i, err)
				}
				if !reflect.DeepEqual(typedResult.Messages(), legacyResult.Messages()) {
					t.Errorf("input %d %#v: typed = %#v, tokens = %#v",
						i, data, typedResult.Messages(), legacyResult.Messages())
				}
			}
		})
	}
}

// TestTypedRules_EquivalenceAcrossCombinedRules checks a multi-rule field, the
// shape real rule sets take, including the nullable short-circuit.
func TestTypedRules_EquivalenceAcrossCombinedRules(t *testing.T) {
	tests := []struct {
		name   string
		tokens []string
		rules  []Rule
		data   []map[string]interface{}
	}{
		{
			name:   "required email",
			tokens: []string{"required", "email"},
			rules:  []Rule{Required(), Email()},
			data:   []map[string]interface{}{{"f": "a@b.com"}, {"f": "nope"}, {"f": ""}},
		},
		{
			name:   "nullable url short circuits",
			tokens: []string{"nullable", "url"},
			rules:  []Rule{Nullable(), URL()},
			data:   []map[string]interface{}{{"f": ""}, {"f": "nope"}, {"f": "https://example.com"}},
		},
		{
			name:   "required nullable contradiction",
			tokens: []string{"required", "nullable"},
			rules:  []Rule{Required(), Nullable()},
			data:   []map[string]interface{}{{"f": ""}, {}},
		},
		{
			name:   "string min max",
			tokens: []string{"string", "min:2", "max:4"},
			rules:  []Rule{String(), Min(2), Max(4)},
			data:   []map[string]interface{}{{"f": "abc"}, {"f": "a"}, {"f": "abcde"}, {"f": 3}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := normalizeRuleSet(RuleSet{"f": tc.rules})
			if err != nil {
				t.Fatalf("normalizeRuleSet: %v", err)
			}
			legacy := normalizedRuleSet{
				fields: map[string][]parsedRule{"f": parseRuleSlice(tc.tokens)},
			}
			if !reflect.DeepEqual(normalized.fields, legacy.fields) {
				t.Fatalf("parsed rules differ:\n typed  = %#v\n tokens = %#v", normalized.fields, legacy.fields)
			}
			for i, data := range tc.data {
				typedResult, err := runNormalized(data, normalized, nil)
				if err != nil {
					t.Fatalf("input %d: typed run: %v", i, err)
				}
				legacyResult, err := runNormalized(data, legacy, nil)
				if err != nil {
					t.Fatalf("input %d: token run: %v", i, err)
				}
				if !reflect.DeepEqual(typedResult.Messages(), legacyResult.Messages()) {
					t.Errorf("input %d %#v: typed = %#v, tokens = %#v",
						i, data, typedResult.Messages(), legacyResult.Messages())
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
			normalized, err := normalizeRuleSet(RuleSet{"f": {tc.rule}})
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
			normalized, err := normalizeRuleSet(RuleSet{"email": {tc.rule}})
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
			normalized, err := normalizeRuleSet(RuleSet{"f": {tc.rule}})
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

// TestTypedRules_RegexAlternationShattersAsToken records why the token form
// could not carry these parameters: the same pattern splits into two rules.
func TestTypedRules_RegexAlternationShattersAsToken(t *testing.T) {
	parsed := parseRuleSlice([]string{"regex:^foo|bar$"})
	if len(parsed) == 1 {
		t.Fatal("token parser no longer splits on '|'; the typed path assumption needs revisiting")
	}
	typed, err := normalizeRuleSet(RuleSet{"f": {Regex("^foo|bar$")}})
	if err != nil {
		t.Fatalf("normalizeRuleSet: %v", err)
	}
	if len(typed.fields["f"]) != 1 {
		t.Fatalf("typed rule count = %d, want 1", len(typed.fields["f"]))
	}
}
