package validation

import (
	"reflect"
	"strings"
	"testing"
)

// constructorCases pins every constructor to the rule name and pre-split
// parameters it must produce. The parameter shapes are the ones the handlers
// in validation/rules (and validation/internal/dbcheck) actually read.
var constructorCases = []struct {
	name   string
	rule   Rule
	want   string
	params []string
}{
	// Presence.
	{name: "Required", rule: Required(), want: "required"},
	{name: "Nullable", rule: Nullable(), want: "nullable"},
	{name: "Filled", rule: Filled(), want: "filled"},
	{name: "Present", rule: Present(), want: "present"},

	// Type.
	{name: "String", rule: String(), want: "string"},
	{name: "Integer", rule: Integer(), want: "integer"},
	{name: "Numeric", rule: Numeric(), want: "numeric"},
	{name: "Boolean", rule: Boolean(), want: "boolean"},
	{name: "Array", rule: Array(), want: "array"},

	// String format.
	{name: "Email", rule: Email(), want: "email"},
	{name: "URL", rule: URL(), want: "url"},
	{name: "URLPublic", rule: URLPublic(), want: "url_public"},
	{name: "Alpha", rule: Alpha(), want: "alpha"},
	{name: "AlphaDash", rule: AlphaDash(), want: "alpha_dash"},
	{name: "AlphaNum", rule: AlphaNum(), want: "alpha_num"},

	// Size.
	{name: "Min", rule: Min(3), want: "min", params: []string{"3"}},
	{name: "Max", rule: Max(10), want: "max", params: []string{"10"}},
	{name: "Size", rule: Size(5), want: "size", params: []string{"5"}},
	{name: "Between", rule: Between(2, 4), want: "between", params: []string{"2", "4"}},

	// Comparison.
	{name: "Same", rule: Same("other"), want: "same", params: []string{"other"}},
	{name: "Different", rule: Different("other"), want: "different", params: []string{"other"}},
	{name: "Confirmed", rule: Confirmed(), want: "confirmed"},
	{name: "Accepted", rule: Accepted(), want: "accepted"},

	// Sets.
	{name: "In", rule: In("a", "b"), want: "in", params: []string{"a", "b"}},
	{name: "NotIn", rule: NotIn("a", "b"), want: "not_in", params: []string{"a", "b"}},

	// Conditional.
	{name: "RequiredIf", rule: RequiredIf("other", "yes"), want: "required_if", params: []string{"other", "yes"}},
	{name: "RequiredUnless", rule: RequiredUnless("other", "yes"), want: "required_unless", params: []string{"other", "yes"}},
	{name: "RequiredWith", rule: RequiredWith("other"), want: "required_with", params: []string{"other"}},
	{name: "RequiredWith multi", rule: RequiredWith("a", "b", "c"), want: "required_with", params: []string{"a", "b", "c"}},
	{name: "RequiredWithout", rule: RequiredWithout("other"), want: "required_without", params: []string{"other"}},
	{name: "RequiredWithout multi", rule: RequiredWithout("a", "b", "c"), want: "required_without", params: []string{"a", "b", "c"}},

	// Date and time.
	{name: "Date", rule: Date(), want: "date"},
	{name: "DateFormat", rule: DateFormat("2006-01-02"), want: "date_format", params: []string{"2006-01-02"}},
	{name: "Timezone", rule: Timezone(), want: "timezone"},

	// Network.
	{name: "IP", rule: IP(), want: "ip"},
	{name: "IPv4", rule: IPv4(), want: "ipv4"},
	{name: "IPv6", rule: IPv6(), want: "ipv6"},

	// Format.
	{name: "Regex", rule: Regex("^[a-z]+$"), want: "regex", params: []string{"^[a-z]+$"}},
	{name: "JSON", rule: JSON(), want: "json"},
	{name: "UUID", rule: UUID(), want: "uuid"},
	{name: "ULID", rule: ULID(), want: "ulid"},
	{name: "StartsWith", rule: StartsWith("foo", "bar"), want: "starts_with", params: []string{"foo", "bar"}},
	{name: "EndsWith", rule: EndsWith(".pdf"), want: "ends_with", params: []string{".pdf"}},
	{name: "Password", rule: Password(), want: "password"},

	// Numeric comparison.
	{name: "Gt", rule: Gt("other"), want: "gt", params: []string{"other"}},
	{name: "Gte", rule: Gte("other"), want: "gte", params: []string{"other"}},
	{name: "Lt", rule: Lt("other"), want: "lt", params: []string{"other"}},
	{name: "Lte", rule: Lte("other"), want: "lte", params: []string{"other"}},

	// File.
	{name: "File", rule: File(), want: "file"},
	{name: "Mimes", rule: Mimes("jpg", "png"), want: "mimes", params: []string{"jpg", "png"}},
	{name: "Image", rule: Image(), want: "image"},
}

func TestConstructors_ProduceExpectedSpec(t *testing.T) {
	for _, tc := range constructorCases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.rule.Rule()
			if spec.Name != tc.want {
				t.Errorf("name = %q, want %q", spec.Name, tc.want)
			}
			if !reflect.DeepEqual(spec.Params, tc.params) {
				t.Errorf("params = %#v, want %#v", spec.Params, tc.params)
			}
			if spec.Handler != nil {
				t.Error("built-in rule must not carry a handler")
			}
		})
	}
}

// TestConstructors_CoverEveryBuiltinRule keeps the constructor surface and the
// engine's built-in registry in lockstep: a rule registered without a
// constructor is unreachable from the typed API.
func TestConstructors_CoverEveryBuiltinRule(t *testing.T) {
	covered := make(map[string]struct{}, len(constructorCases))
	for _, tc := range constructorCases {
		covered[tc.want] = struct{}{}
	}
	for name := range builtinRuleNames {
		if _, ok := covered[name]; !ok {
			t.Errorf("built-in rule %q has no constructor", name)
		}
	}
	for name := range covered {
		if _, ok := builtinRuleNames[name]; !ok {
			t.Errorf("constructor produces %q, which is not a registered built-in", name)
		}
	}
	if len(covered) != len(builtinRuleNames) {
		t.Errorf("constructor count = %d, built-in count = %d", len(covered), len(builtinRuleNames))
	}
}

func TestPasswordSpec_MinLength(t *testing.T) {
	base := Password()
	strong := base.MinLength(12)

	if got := base.Rule().Params; got != nil {
		t.Errorf("base params = %#v, want nil (builder mutated the receiver)", got)
	}
	if got := strong.Rule().Params; !reflect.DeepEqual(got, []string{"12"}) {
		t.Errorf("params = %#v, want [12]", got)
	}
	if got := base.MinLength(0).Rule().Params; got != nil {
		t.Errorf("non-positive length must keep the built-in floor, got %#v", got)
	}
}

func TestMimesSpec_AllowSVG(t *testing.T) {
	base := Mimes("jpg", "svg")
	opted := base.AllowSVG()

	if got := base.Rule().Params; !reflect.DeepEqual(got, []string{"jpg", "svg"}) {
		t.Errorf("base params = %#v, want [jpg svg] (builder mutated the receiver)", got)
	}
	if got := opted.Rule().Params; !reflect.DeepEqual(got, []string{"jpg", "svg", "allow_svg"}) {
		t.Errorf("params = %#v, want [jpg svg allow_svg]", got)
	}
}

func TestImageSpec_AllowSVG(t *testing.T) {
	base := Image()
	opted := base.AllowSVG()

	if got := base.Rule().Params; got != nil {
		t.Errorf("base params = %#v, want nil (builder mutated the receiver)", got)
	}
	if got := opted.Rule().Params; !reflect.DeepEqual(got, []string{"allow_svg"}) {
		t.Errorf("params = %#v, want [allow_svg]", got)
	}
}

func TestUniqueSpec_Builders(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		want []string
	}{
		{name: "table and column", rule: Unique("users", "email"), want: []string{"users", "email"}},
		{name: "column defaults to field", rule: Unique("users", ""), want: []string{"users", ""}},
		{name: "except int", rule: Unique("users", "email").Except(7), want: []string{"users", "email", "7"}},
		{name: "except string", rule: Unique("users", "email").Except("u-1"), want: []string{"users", "email", "u-1"}},
		{name: "except with id column", rule: Unique("users", "email").Except(7).IDColumn("uid"), want: []string{"users", "email", "7", "uid"}},
		{name: "id column without except", rule: Unique("users", "email").IDColumn("uid"), want: []string{"users", "email"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.rule.Rule()
			if spec.Name != "unique" {
				t.Errorf("name = %q, want unique", spec.Name)
			}
			if !reflect.DeepEqual(spec.Params, tc.want) {
				t.Errorf("params = %#v, want %#v", spec.Params, tc.want)
			}
		})
	}
}

func TestUniqueSpec_BuildersDoNotMutateReceiver(t *testing.T) {
	base := Unique("users", "email")
	withExcept := base.Except(7)
	withColumn := withExcept.IDColumn("uid")

	if got := base.Rule().Params; !reflect.DeepEqual(got, []string{"users", "email"}) {
		t.Errorf("base params = %#v, want [users email]", got)
	}
	if got := withExcept.Rule().Params; !reflect.DeepEqual(got, []string{"users", "email", "7"}) {
		t.Errorf("withExcept params = %#v, want [users email 7]", got)
	}
	if got := withColumn.Rule().Params; !reflect.DeepEqual(got, []string{"users", "email", "7", "uid"}) {
		t.Errorf("withColumn params = %#v, want [users email 7 uid]", got)
	}
}

// stringerID exercises the fmt.Stringer branch of Except.
type stringerID struct{ v string }

func (s stringerID) String() string { return s.v }

func TestExceptParam_AcceptedTypes(t *testing.T) {
	tests := []struct {
		name string
		id   interface{}
		want string
	}{
		{name: "string", id: "abc", want: "abc"},
		{name: "int", id: 42, want: "42"},
		{name: "int8", id: int8(8), want: "8"},
		{name: "int16", id: int16(16), want: "16"},
		{name: "int32", id: int32(32), want: "32"},
		{name: "int64", id: int64(64), want: "64"},
		{name: "uint", id: uint(1), want: "1"},
		{name: "uint8", id: uint8(2), want: "2"},
		{name: "uint16", id: uint16(3), want: "3"},
		{name: "uint32", id: uint32(4), want: "4"},
		{name: "uint64", id: uint64(5), want: "5"},
		{name: "stringer", id: stringerID{v: "uuid-1"}, want: "uuid-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exceptParam(tc.id)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExceptParam_RejectsUnusableTypes(t *testing.T) {
	for _, id := range []interface{}{3.5, true, []string{"a"}, struct{}{}, nil} {
		if _, err := exceptParam(id); err == nil {
			t.Errorf("exceptParam(%#v) = nil error, want rejection", id)
		}
	}
}

// TestUniqueSpec_RuleWithUnusableExcept documents the rendering fallback for a
// spec that normalization has already rejected: the parameter is dropped
// rather than rendered from an unusable value.
func TestUniqueSpec_RuleWithUnusableExcept(t *testing.T) {
	spec := Unique("users", "email").Except(3.5).Rule()
	if !reflect.DeepEqual(spec.Params, []string{"users", "email", ""}) {
		t.Errorf("params = %#v, want [users email ]", spec.Params)
	}
}

func TestExists_Spec(t *testing.T) {
	spec := Exists("users", "id").Rule()
	if spec.Name != "exists" {
		t.Errorf("name = %q, want exists", spec.Name)
	}
	if !reflect.DeepEqual(spec.Params, []string{"users", "id"}) {
		t.Errorf("params = %#v, want [users id]", spec.Params)
	}
}

func TestCustom_CarriesHandler(t *testing.T) {
	handler := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		return nil
	}
	spec := Custom("even", handler).Rule()
	if spec.Name != "even" {
		t.Errorf("name = %q, want even", spec.Name)
	}
	if spec.Handler == nil {
		t.Fatal("handler was not carried")
	}
	if spec.Params != nil {
		t.Errorf("params = %#v, want nil", spec.Params)
	}
}

// TestFieldListConstructors_RequireAFirstField pins the shape that makes an
// empty field list unrepresentable: the first field is a separate parameter,
// so RequiredWith() does not compile. The rest fold into the same parameter
// list, in order.
func TestFieldListConstructors_RequireAFirstField(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		want []string
	}{
		{name: "required_with single", rule: RequiredWith("a"), want: []string{"a"}},
		{name: "required_with multi", rule: RequiredWith("a", "b", "c"), want: []string{"a", "b", "c"}},
		{name: "required_without single", rule: RequiredWithout("a"), want: []string{"a"}},
		{name: "required_without multi", rule: RequiredWithout("a", "b", "c"), want: []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Rule().Params; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("params = %#v, want %#v", got, tc.want)
			}
		})
	}

	// The additional fields are copied, like every other variadic constructor.
	additional := []string{"b", "c"}
	rule := RequiredWith("a", additional...)
	additional[0] = "mutated"
	if got := rule.Rule().Params; !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("params = %#v, want [a b c]", got)
	}
}

// TestVariadicConstructors_CopyCallerSlice guards immutability: a caller that
// spreads its own slice must not be able to rewrite the rule afterwards.
func TestVariadicConstructors_CopyCallerSlice(t *testing.T) {
	values := []string{"a", "b"}
	rule := In(values...)
	mimes := Mimes(values...)
	values[0] = "mutated"

	if got := rule.Rule().Params; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("In params = %#v, want [a b]", got)
	}
	if got := mimes.Rule().Params; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Mimes params = %#v, want [a b]", got)
	}
}

// TestRules_IsContractType pins the alias identity: adopter code writes
// validation.Rules and must satisfy interfaces declared against the
// contract type.
func TestRules_IsContractType(t *testing.T) {
	var rs Rules = Rules{"email": {Required(), Email()}}
	if len(rs["email"]) != 2 {
		t.Fatalf("rule count = %d, want 2", len(rs["email"]))
	}
	names := make([]string, 0, 2)
	for _, r := range rs["email"] {
		names = append(names, r.Rule().Name)
	}
	if got := strings.Join(names, ","); got != "required,email" {
		t.Errorf("names = %q, want required,email", got)
	}
}
