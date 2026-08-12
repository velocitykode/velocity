package validation

import (
	"fmt"
	"strconv"

	"github.com/velocitykode/velocity/contract"
)

// Rule describes a single validation rule. Canonical declaration lives in
// the stdlib-only contract leaf.
type Rule = contract.ValidationRule

// Rules maps field names to the rules applied to that field. Canonical
// declaration lives in the contract leaf as ValidationRuleSet.
//
//	rules := validation.Rules{
//	    "email":    {validation.Required(), validation.Email()},
//	    "password": {validation.Required(), validation.Min(8), validation.Confirmed()},
//	}
type Rules = contract.ValidationRuleSet

// MessageKey addresses one field+rule pair for message overrides.
type MessageKey = contract.ValidationMessageKey

// Messages maps a field+rule pair to a replacement message.
//
//	messages := validation.Messages{
//	    {Field: "email", Rule: "required"}: "We need your email.",
//	}
type Messages = contract.ValidationMessages

// simpleRule is the immutable value behind every parameter-complete
// constructor. Its params slice is never mutated after construction, so one
// value is safe to share across goroutines and rule sets.
type simpleRule struct {
	name   string
	params []string
}

// Rule implements contract.ValidationRule.
func (r simpleRule) Rule() contract.ValidationRuleSpec {
	return contract.ValidationRuleSpec{Name: r.name, Params: r.params}
}

// newRule builds a rule with no parameters.
func newRule(name string) Rule {
	return simpleRule{name: name}
}

// newRuleWith builds a rule with the given parameters. params is copied so a
// caller that spreads its own slice cannot mutate the rule afterwards.
func newRuleWith(name string, params ...string) Rule {
	return simpleRule{name: name, params: append([]string(nil), params...)}
}

// fieldList joins a required first field with the optional rest into one
// parameter slice the caller cannot mutate afterwards.
func fieldList(field string, additional []string) []string {
	params := make([]string, 0, 1+len(additional))
	params = append(params, field)
	return append(params, additional...)
}

// Presence rules.

// Required requires the field to be present and non-empty.
func Required() Rule { return newRule("required") }

// Nullable marks the field optional: when its value is nil or "", every
// other rule on that field is skipped, including Required.
func Nullable() Rule { return newRule("nullable") }

// Filled requires the field to be non-empty when it is present.
func Filled() Rule { return newRule("filled") }

// Present requires the field key to exist, even with an empty value.
func Present() Rule { return newRule("present") }

// Type rules.

// String requires a string value.
func String() Rule { return newRule("string") }

// Integer requires an integer value.
func Integer() Rule { return newRule("integer") }

// Numeric requires a numeric value.
func Numeric() Rule { return newRule("numeric") }

// Boolean requires a boolean value.
func Boolean() Rule { return newRule("boolean") }

// Array requires a slice or array value.
func Array() Rule { return newRule("array") }

// String-format rules.

// Email requires a valid email address.
func Email() Rule { return newRule("email") }

// URL requires a valid URL.
func URL() Rule { return newRule("url") }

// URLPublic requires a URL that resolves to a public host.
func URLPublic() Rule { return newRule("url_public") }

// Alpha requires alphabetic characters only.
func Alpha() Rule { return newRule("alpha") }

// AlphaDash requires alphanumeric characters, dashes, and underscores.
func AlphaDash() Rule { return newRule("alpha_dash") }

// AlphaNum requires alphanumeric characters only.
func AlphaNum() Rule { return newRule("alpha_num") }

// Size rules. For strings n is a character count, for numbers a value, and
// for slices an element count.

// Min requires a minimum size of n.
func Min(n int) Rule { return newRuleWith("min", strconv.Itoa(n)) }

// Max requires a maximum size of n.
func Max(n int) Rule { return newRuleWith("max", strconv.Itoa(n)) }

// Size requires an exact size of n.
func Size(n int) Rule { return newRuleWith("size", strconv.Itoa(n)) }

// Between requires a size within [min, max].
func Between(min, max int) Rule {
	return newRuleWith("between", strconv.Itoa(min), strconv.Itoa(max))
}

// Comparison rules.

// Same requires the value to equal the value of field.
func Same(field string) Rule { return newRuleWith("same", field) }

// Different requires the value to differ from the value of field.
func Different(field string) Rule { return newRuleWith("different", field) }

// Confirmed requires a matching "<field>_confirmation" sibling field.
func Confirmed() Rule { return newRule("confirmed") }

// Accepted requires a truthy value ("yes", "on", "1", "true", true, 1).
func Accepted() Rule { return newRule("accepted") }

// Set rules.

// In requires the value to be one of values.
func In(values ...string) Rule { return newRuleWith("in", values...) }

// NotIn requires the value to be none of values.
func NotIn(values ...string) Rule { return newRuleWith("not_in", values...) }

// Conditional rules.

// RequiredIf requires the field when field equals value.
func RequiredIf(field, value string) Rule {
	return newRuleWith("required_if", field, value)
}

// RequiredUnless requires the field unless field equals value.
func RequiredUnless(field, value string) Rule {
	return newRuleWith("required_unless", field, value)
}

// RequiredWith requires the field when any of the named fields is present.
// The first field is a separate parameter so an empty field list cannot be
// expressed.
func RequiredWith(field string, additional ...string) Rule {
	return simpleRule{name: "required_with", params: fieldList(field, additional)}
}

// RequiredWithout requires the field when any of the named fields is absent.
// The first field is a separate parameter so an empty field list cannot be
// expressed.
func RequiredWithout(field string, additional ...string) Rule {
	return simpleRule{name: "required_without", params: fieldList(field, additional)}
}

// Date and time rules.

// Date requires a value parseable as a date in one of the common layouts.
func Date() Rule { return newRule("date") }

// DateFormat requires a value parseable with the Go reference-time layout,
// e.g. "2006-01-02". The layout is carried verbatim in one parameter, so
// layouts containing ',' or ':' are preserved.
func DateFormat(layout string) Rule { return newRuleWith("date_format", layout) }

// Timezone requires a valid IANA timezone identifier.
func Timezone() Rule { return newRule("timezone") }

// Network rules.

// IP requires a valid IPv4 or IPv6 address.
func IP() Rule { return newRule("ip") }

// IPv4 requires a valid IPv4 address.
func IPv4() Rule { return newRule("ipv4") }

// IPv6 requires a valid IPv6 address.
func IPv6() Rule { return newRule("ipv6") }

// Format rules.

// Regex requires the value to match pattern, which is anchored by the rule.
// The pattern is carried verbatim in one parameter, so '|' alternations and
// ',' quantifiers are preserved.
func Regex(pattern string) Rule { return newRuleWith("regex", pattern) }

// JSON requires a value that parses as JSON.
func JSON() Rule { return newRule("json") }

// UUID requires a valid UUID.
func UUID() Rule { return newRule("uuid") }

// ULID requires a valid ULID.
func ULID() Rule { return newRule("ulid") }

// StartsWith requires the value to begin with one of prefixes.
func StartsWith(prefixes ...string) Rule {
	return newRuleWith("starts_with", prefixes...)
}

// EndsWith requires the value to end with one of suffixes.
func EndsWith(suffixes ...string) Rule {
	return newRuleWith("ends_with", suffixes...)
}

// Numeric comparison rules. The parameter is another field's name, or a
// numeric literal: the rule resolves a parseable number before looking the
// name up in the input data.

// Gt requires the value to be greater than field.
func Gt(field string) Rule { return newRuleWith("gt", field) }

// Gte requires the value to be greater than or equal to field.
func Gte(field string) Rule { return newRuleWith("gte", field) }

// Lt requires the value to be less than field.
func Lt(field string) Rule { return newRuleWith("lt", field) }

// Lte requires the value to be less than or equal to field.
func Lte(field string) Rule { return newRuleWith("lte", field) }

// File rules. These operate on file-upload metadata merged into the input
// data, not on the raw request.

// File requires a file-upload value.
func File() Rule { return newRule("file") }

// PasswordSpec is the immutable rule value produced by Password. Its
// builder method returns a new value and never mutates the receiver.
type PasswordSpec struct {
	minLength int
}

// Password requires a value with at least 8 characters, mixed case, one
// digit, and one symbol.
func Password() PasswordSpec { return PasswordSpec{} }

// MinLength returns a copy of the rule requiring at least n characters.
// Values below 1 leave the built-in floor of 8 in place.
func (p PasswordSpec) MinLength(n int) PasswordSpec {
	p.minLength = n
	return p
}

// Rule implements contract.ValidationRule.
func (p PasswordSpec) Rule() contract.ValidationRuleSpec {
	spec := contract.ValidationRuleSpec{Name: "password"}
	if p.minLength > 0 {
		spec.Params = []string{strconv.Itoa(p.minLength)}
	}
	return spec
}

// MimesSpec is the immutable rule value produced by Mimes. Its builder
// method returns a new value and never mutates the receiver.
type MimesSpec struct {
	exts     []string
	allowSVG bool
}

// Mimes requires an uploaded file whose extension is one of exts and whose
// sniffed content matches that extension. SVG uploads additionally require
// AllowSVG.
func Mimes(exts ...string) MimesSpec {
	return MimesSpec{exts: append([]string(nil), exts...)}
}

// AllowSVG returns a copy of the rule that accepts script-free SVG uploads.
func (m MimesSpec) AllowSVG() MimesSpec {
	m.allowSVG = true
	return m
}

// Rule implements contract.ValidationRule.
func (m MimesSpec) Rule() contract.ValidationRuleSpec {
	params := m.exts
	if m.allowSVG {
		params = append(append([]string(nil), m.exts...), "allow_svg")
	}
	return contract.ValidationRuleSpec{Name: "mimes", Params: params}
}

// ImageSpec is the immutable rule value produced by Image. Its builder
// method returns a new value and never mutates the receiver.
type ImageSpec struct {
	allowSVG bool
}

// Image requires an uploaded image whose sniffed content matches its
// extension. SVG uploads require AllowSVG.
func Image() ImageSpec { return ImageSpec{} }

// AllowSVG returns a copy of the rule that accepts script-free SVG uploads.
func (i ImageSpec) AllowSVG() ImageSpec {
	i.allowSVG = true
	return i
}

// Rule implements contract.ValidationRule.
func (i ImageSpec) Rule() contract.ValidationRuleSpec {
	spec := contract.ValidationRuleSpec{Name: "image"}
	if i.allowSVG {
		spec.Params = []string{"allow_svg"}
	}
	return spec
}

// Database rules. These only describe the check; execution lives in
// validation/dbrules, which owns the orm dependency.

// UniqueSpec is the immutable rule value produced by Unique. Builder
// methods return a new value and never mutate the receiver.
type UniqueSpec struct {
	table     string
	column    string
	except    interface{}
	hasExcept bool
	idColumn  string
}

// Unique requires the value to be absent from table.column. An empty
// column defaults to the field name at evaluation time.
func Unique(table, column string) UniqueSpec {
	return UniqueSpec{table: table, column: column}
}

// Except returns a copy of the rule that ignores the row whose id column
// holds id. id must be an integer, a string, or a fmt.Stringer; anything
// else is reported when the rule set is normalized.
func (u UniqueSpec) Except(id interface{}) UniqueSpec {
	u.except = id
	u.hasExcept = true
	return u
}

// IDColumn returns a copy of the rule that matches Except against the named
// column instead of "id". It has no effect without Except.
func (u UniqueSpec) IDColumn(name string) UniqueSpec {
	u.idColumn = name
	return u
}

// Rule implements contract.ValidationRule.
func (u UniqueSpec) Rule() contract.ValidationRuleSpec {
	params := []string{u.table, u.column}
	if u.hasExcept {
		except, err := exceptParam(u.except)
		if err != nil {
			// Unreachable through a normalized rule set: normalization
			// rejects the value before the engine asks for the spec.
			except = ""
		}
		params = append(params, except)
		if u.idColumn != "" {
			params = append(params, u.idColumn)
		}
	}
	return contract.ValidationRuleSpec{Name: "unique", Params: params}
}

// validateRule reports an Except value the unique rule cannot carry.
func (u UniqueSpec) validateRule() error {
	if !u.hasExcept {
		return nil
	}
	_, err := exceptParam(u.except)
	return err
}

// Exists requires the value to be present in table.column. An empty column
// defaults to the field name at evaluation time.
func Exists(table, column string) Rule {
	return newRuleWith("exists", table, column)
}

// exceptParam renders a Unique().Except() argument as a query parameter.
// Only integers, strings, and fmt.Stringer values are accepted: anything
// else has no unambiguous SQL comparison value.
func exceptParam(id interface{}) (string, error) {
	switch v := id.(type) {
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case fmt.Stringer:
		return v.String(), nil
	default:
		return "", fmt.Errorf("%w: unique except value of type %T must be an integer, string, or fmt.Stringer", ErrInvalidRule, id)
	}
}

// customRule carries its own handler so it runs on any validator the rule
// set reaches, including the per-request validator built by the Check
// helpers.
type customRule struct {
	name    string
	handler RuleHandler
}

// Custom builds a rule backed by handler. The handler travels with the rule
// value, so no prior registration on a long-lived validator is required.
// A nil handler, or a name that shadows a built-in rule, is reported when
// the rule set is normalized.
func Custom(name string, handler RuleHandler) Rule {
	return customRule{name: name, handler: handler}
}

// Rule implements contract.ValidationRule.
func (c customRule) Rule() contract.ValidationRuleSpec {
	return contract.ValidationRuleSpec{Name: c.name, Handler: c.handler}
}

// carriesHandler marks customRule so normalization can tell a custom rule
// with a nil handler from a built-in rule, which legitimately has none.
func (c customRule) carriesHandler() {}
