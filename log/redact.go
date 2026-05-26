package log

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Redactor mutates a single string immediately before it is handed to
// a Logger driver. The contract is intentionally narrow: rewrite the
// fragment in place, return the rewritten value. Drivers call every
// configured redactor against the message and each kv key / value
// before final emission, so a single registered redactor protects
// every sink (file, console, stack child, third-party driver).
//
// The redaction layer is deliberately downstream of the sanitiser
// (log/internal/sanitize): sanitisation defends the structure of the
// log line (no forged records via CRLF, no ANSI hijack), redaction
// defends the content (no secrets / PII leak into the log file). The
// two are independent and stack.
//
// Implementations MUST be safe for concurrent use by multiple
// goroutines (file + console + stack callers all log concurrently).
// Implementations MUST NOT panic; a misbehaving redactor that takes
// down the logger turns a hardening control into a denial-of-service
// vector. Implementations MUST run in bounded time; this package
// only ships RE2-backed regexp redactors so the ReDoS class is
// closed by construction.
type Redactor interface {
	// Redact returns s with sensitive substrings replaced. If
	// nothing matches, the original string is returned unchanged
	// (drivers rely on this to skip allocations on the common path).
	Redact(s string) string
}

// RedactorFunc adapts an ordinary function to the Redactor interface
// so callers can register an inline rule without declaring a type.
type RedactorFunc func(string) string

// Redact satisfies the Redactor interface for RedactorFunc.
func (f RedactorFunc) Redact(s string) string { return f(s) }

// Chain returns a Redactor that applies each rs in order, threading
// the output of each through the next. An empty chain is a no-op
// that returns the input untouched.
//
// Chain is the entry point Velocity drivers reach for: a single call
// site applies every registered redactor in priority order. Order
// matters because earlier rules can mask substrings later rules
// might otherwise see (e.g. redacting an Authorization header value
// to [REDACTED] hides any embedded JWT from the JWT redactor, which
// is the intended outcome).
func Chain(rs ...Redactor) Redactor {
	if len(rs) == 0 {
		return RedactorFunc(func(s string) string { return s })
	}
	if len(rs) == 1 {
		return rs[0]
	}
	chain := make([]Redactor, len(rs))
	copy(chain, rs)
	return RedactorFunc(func(s string) string {
		for _, r := range chain {
			s = r.Redact(s)
		}
		return s
	})
}

// ---- HTTP header redactor -------------------------------------------------

// fullValueSensitiveHeaders is the list of HTTP header names whose
// entire value half (everything up to CR / LF / end-of-input) is
// replaced with [REDACTED]. The Cookie header concatenates multiple
// credentials with ';' separators ("session=a; csrf=b; remember=c"),
// so stopping at the first ';' would leak every cookie after the
// first one. Authorization, Proxy-Authorization, and X-* token /
// API-key headers carry a single value with no internal ';' boundary,
// so end-of-line is the only correct stop in either case.
//
// Matched case-insensitively.
var fullValueSensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Proxy-Authorization",
	"X-API-Key",
	"X-Auth-Token",
	"X-Csrf-Token",
}

// pairValueSensitiveHeaders is the list of HTTP header names whose
// value half is redacted only up to the first ';' separator, so the
// cookie attributes after it (Path, Domain, HttpOnly, Secure,
// SameSite, Max-Age, Expires) survive into the log.
//
// Approach (B) from the M-38 finding: attribute preservation is a
// real observability signal. Operators verify "is my session cookie
// actually HttpOnly + Secure + SameSite=Lax in production?" by
// scraping logs. The credential (name=value) before the first ';' is
// the secret; everything after is public metadata about how the
// browser is told to handle it. Approach (A) "redact entire value"
// would erase that signal.
//
// Matched case-insensitively.
var pairValueSensitiveHeaders = []string{
	"Set-Cookie",
}

// headerNameBoundary is the prefix used in front of every header name
// alternative so a substring match never fires inside a compound
// header. "Cookie" must not match the trailing "Cookie" inside
// "Set-Cookie" (Set-Cookie uses pair-value redaction, Cookie uses
// full-value), so `\b` is not strong enough since `-` is a word
// boundary in Go's RE2. The wrapper requires the byte before the
// header name to be start-of-input / start-of-line OR any character
// that is not part of an HTTP header token (per RFC 7230 token =
// 1*tchar; we exclude letters, digits, '-', and '_' which are the
// chars HTTP header names actually use).
const headerNameBoundary = `(?:^|[^A-Za-z0-9_-])`

// headerFullValueRedactor matches "Header: value" pairs where the
// header is in fullValueSensitiveHeaders and rewrites the value to
// [REDACTED]. The value half spans everything up to CR / LF /
// end-of-input so the Cookie header's internal ';' separators no
// longer truncate the redaction at the first cookie pair.
//
// Regex is RE2-backed (no backreferences) with a bounded character
// class for the value half, so the engine is linear-time and the
// ReDoS class is unreachable.
var headerFullValueRedactor = func() *regexp.Regexp {
	parts := make([]string, len(fullValueSensitiveHeaders))
	for i, name := range fullValueSensitiveHeaders {
		parts[i] = regexp.QuoteMeta(name)
	}
	// (?i) case-insensitive header name; value is anything up to CR /
	// LF / end-of-input. The character class is bounded so the engine
	// cannot backtrack catastrophically.
	pattern := `(?im)` + headerNameBoundary + `(` + strings.Join(parts, "|") + `)\s*[:=]\s*[^\r\n]+`
	return regexp.MustCompile(pattern)
}()

// headerPairValueRedactor matches "Header: name=value" pairs where the
// header is in pairValueSensitiveHeaders and rewrites the value up to
// the first ';' (preserving Set-Cookie attributes that follow).
//
// Same RE2-backed bounded character class as the full-value regex.
var headerPairValueRedactor = func() *regexp.Regexp {
	parts := make([]string, len(pairValueSensitiveHeaders))
	for i, name := range pairValueSensitiveHeaders {
		parts[i] = regexp.QuoteMeta(name)
	}
	pattern := `(?im)` + headerNameBoundary + `(` + strings.Join(parts, "|") + `)\s*[:=]\s*[^\r\n;]+`
	return regexp.MustCompile(pattern)
}()

// HeaderRedactor returns a Redactor that replaces the value half of
// any sensitive HTTP header with [REDACTED]. The header name is
// preserved verbatim so operators can still see which header fired
// (the contents are gone, the context is kept).
//
// Two redaction shapes are applied in sequence:
//
//  1. Full-value redaction for Authorization, Cookie,
//     Proxy-Authorization, X-API-Key, X-Auth-Token, X-Csrf-Token.
//     The Cookie header concatenates "session=a; csrf=b; remember=c";
//     stopping at the first ';' (the pre-M-38 behaviour) leaked every
//     cookie after the first. End-of-line is the only safe stop.
//
//  2. Pair-only redaction for Set-Cookie (approach B): only the
//     name=value pair before the first ';' is replaced, so cookie
//     attributes (Path, HttpOnly, Secure, SameSite, Domain, Max-Age,
//     Expires) survive. Operators read those attributes to verify
//     cookie-security configuration in production logs; redacting
//     them erases real observability signal while the credential
//     itself sits before the ';'.
//
// Matches both colon-separated ("Authorization: Bearer ...") and
// equals-separated ("Cookie=session=...") shapes so URL-encoded query
// dumps and structured kv lines both get filtered.
func HeaderRedactor() Redactor {
	return RedactorFunc(func(s string) string {
		s = headerFullValueRedactor.ReplaceAllStringFunc(s, redactHeaderMatch)
		s = headerPairValueRedactor.ReplaceAllStringFunc(s, redactHeaderMatch)
		return s
	})
}

// redactHeaderMatch rewrites a matched "Header: value" (or
// "Header=value") fragment so the header name and separator are
// preserved and the value is replaced with [REDACTED]. Shared between
// the full-value and pair-value regex callbacks because the
// header-name preservation logic is identical; only the regex bounds
// where "value" ends differ.
//
// The regex prefix headerNameBoundary captures either start-of-line
// (empty) or one boundary byte (a non-header-token char that we must
// echo back so we do not silently delete e.g. a leading newline or
// space between log fields). The first ':' or '=' marks the end of
// the header name; everything after is the value to redact.
func redactHeaderMatch(match string) string {
	for i := 0; i < len(match); i++ {
		if match[i] == ':' || match[i] == '=' {
			return match[:i+1] + " [REDACTED]"
		}
	}
	return "[REDACTED]"
}

// ---- JWT redactor ---------------------------------------------------------

// jwtRegex matches a 3-segment JWT: base64url header "." base64url
// payload "." base64url signature. The header MUST start with "eyJ"
// (the base64url encoding of '{"' which begins every JSON object) so
// arbitrary three-dot sequences (file paths, version strings) are not
// false-matched. Each segment is bounded with a max length so the
// regex is linear-time in the input.
//
// Lengths: header rarely exceeds 200 bytes, payload rarely exceeds
// 4 KiB, signature is fixed-shape per alg. 1024-byte caps comfortably
// fit every realistic JWT while bounding the engine's work to O(n).
// Go's regexp engine caps explicit repeat counts at 1000 by default;
// we stay just under that ceiling.
var jwtRegex = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{1,1000}\.eyJ[A-Za-z0-9_-]{1,1000}\.[A-Za-z0-9_-]{1,1000}`)

// JWTRedactor returns a Redactor that replaces every base64url JWT
// triple with the literal [JWT]. The shape requirement (eyJ. eyJ.)
// is strict enough to avoid false positives on dotted identifiers
// while still catching the canonical "header.payload.signature"
// emitted by every standards-compliant signer.
func JWTRedactor() Redactor {
	return RedactorFunc(func(s string) string {
		return jwtRegex.ReplaceAllString(s, "[JWT]")
	})
}

// ---- PAN (Primary Account Number) redactor --------------------------------

// panCandidateRegex matches any 13-19 contiguous-digit run NOT bordered
// by other digits. The bounded length plus possessive-free pattern is
// linear-time under RE2. Luhn validation runs on every candidate; only
// Luhn-valid candidates are rewritten to keep false positives low.
//
// We deliberately use \b boundaries so a 20-digit identifier (longer
// than the longest issued PAN, 19) is not partially matched. The
// regex stays digit-only; PANs with embedded spaces / dashes are
// caught by a second pass that strips separators before validating.
var panCandidateRegex = regexp.MustCompile(`\b\d{13,19}\b`)

// panSeparatedRegex matches the human-readable PAN shape (groups of
// 4 digits separated by single space or dash). The leading and
// trailing boundaries keep "1234-5678-9012-3456" inside an arbitrary
// sentence findable while not partially matching longer digit runs.
var panSeparatedRegex = regexp.MustCompile(`\b(?:\d{4}[-\s]){3,4}\d{1,4}\b`)

// PANRedactor returns a Redactor that replaces every Luhn-valid
// 13-19 digit run (with or without space/dash separators) with the
// literal [CARD]. The Luhn check keeps false-positive noise down so
// random-looking numeric identifiers (order IDs, timestamps) pass
// through.
func PANRedactor() Redactor {
	return RedactorFunc(func(s string) string {
		// Pass 1: separated form (handle "4111 1111 1111 1111" /
		// "4111-1111-1111-1111"). Run first because pass 2's digit-only
		// match cannot see across the separator characters.
		s = panSeparatedRegex.ReplaceAllStringFunc(s, func(match string) string {
			digits := stripNonDigits(match)
			if len(digits) < 13 || len(digits) > 19 {
				return match
			}
			if !luhnValid(digits) {
				return match
			}
			return "[CARD]"
		})
		// Pass 2: contiguous digits with no separators.
		return panCandidateRegex.ReplaceAllStringFunc(s, func(match string) string {
			if !luhnValid(match) {
				return match
			}
			return "[CARD]"
		})
	})
}

// stripNonDigits returns s with every non-ASCII-digit byte removed.
func stripNonDigits(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// luhnValid reports whether the digit string passes the Luhn check.
// Empty input is rejected. Non-digit input is rejected (caller is
// expected to pre-strip). Algorithm: double every second digit from
// the right, sum digit values, mod 10 == 0.
func luhnValid(digits string) bool {
	if len(digits) == 0 {
		return false
	}
	sum := 0
	parity := len(digits) % 2
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if i%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// ---- Email redactor (opt-in) ----------------------------------------------

// emailRegex matches RFC-5322-ish "local@domain.tld" pairs. The
// pattern is intentionally permissive on the local-part but bounds
// every character class with a length cap (256 bytes per RFC) so the
// engine stays linear. RE2's lack of backreferences forecloses the
// classic email-regex ReDoS family.
var emailRegex = regexp.MustCompile(`[A-Za-z0-9._%+\-]{1,64}@[A-Za-z0-9.\-]{1,255}\.[A-Za-z]{2,24}`)

// EmailRedactor returns a Redactor that replaces every email address
// with [EMAIL]. Off by default; opt in either by registering it
// explicitly via WithRedactors(...) or by setting LOG_REDACT_EMAILS=true
// in the environment (BuildDefaultRedactors picks it up).
//
// Email redaction is opt-in because email addresses are often the
// primary debug breadcrumb in a request log; replacing them with
// [EMAIL] makes correlating user reports to log lines materially
// harder. PII regimes that require it (GDPR Art. 32 pseudonymisation
// arguments, certain HIPAA contexts) get to flip the flag.
func EmailRedactor() Redactor {
	return RedactorFunc(func(s string) string {
		return emailRegex.ReplaceAllString(s, "[EMAIL]")
	})
}

// ---- Default redactor chain ----------------------------------------------

// defaultRedactorChain caches the result of BuildDefaultRedactors so
// the env lookup and chain composition only happens once per process.
// Protected by sync.Once because every NewFileLogger / NewConsoleLogger
// call reads it. Loaded eagerly on first New call.
var (
	defaultRedactorOnce  sync.Once
	defaultRedactorCache Redactor
)

// BuildDefaultRedactors returns the framework's recommended redactor
// chain. Always includes HeaderRedactor, JWTRedactor, and PANRedactor.
// Includes EmailRedactor when LOG_REDACT_EMAILS=true.
//
// The result is cached after the first call so subsequent calls return
// the same chain instance (allocation-free fast path). Operators who
// want a custom set should call the individual redactor constructors
// and pass them through WithRedactors directly.
func BuildDefaultRedactors() Redactor {
	defaultRedactorOnce.Do(func() {
		rs := []Redactor{
			HeaderRedactor(),
			JWTRedactor(),
			PANRedactor(),
		}
		if strings.EqualFold(os.Getenv("LOG_REDACT_EMAILS"), "true") {
			rs = append(rs, EmailRedactor())
		}
		defaultRedactorCache = Chain(rs...)
	})
	return defaultRedactorCache
}

// redactingLogger wraps any Logger and applies a Redactor chain to
// every message and kv pair before delegating to the inner logger.
// Wrapping at this layer (rather than in each driver) means a single
// registered chain protects file, console, stack-children, and any
// third-party driver registered through Drivers() identically.
//
// The wrapper is transparent: Shutdown is forwarded to the inner
// logger if it implements Shutdowner, so the embedded driver still
// closes file handles on app shutdown.
type redactingLogger struct {
	inner    Logger
	redactor Redactor
}

// WithRedactors wraps logger so every Debug/Info/Warn/Error/Fatal call
// runs its msg and every kv key+value through redactors in order
// before delegation. Passing zero redactors is a no-op that returns
// logger unchanged.
//
// Callers should compose this around any *FileLogger, *ConsoleLogger,
// *StackLogger, or third-party driver they construct. The framework
// wires it automatically via the LogConfig "redact": true option;
// direct API users invoke it explicitly.
func WithRedactors(logger Logger, redactors ...Redactor) Logger {
	if logger == nil {
		return nil
	}
	if len(redactors) == 0 {
		return logger
	}
	return &redactingLogger{inner: logger, redactor: Chain(redactors...)}
}

// redact applies the chain to msg and every kv pair, returning the
// rewritten values. kvs is processed in pairs (key, value, key, ...);
// an odd trailing element is sanitised too so a single stray arg
// cannot smuggle a secret past the chain.
func (r *redactingLogger) redact(msg string, kvs []any) (string, []any) {
	rMsg := r.redactor.Redact(msg)
	if len(kvs) == 0 {
		return rMsg, kvs
	}
	out := make([]any, len(kvs))
	for i, v := range kvs {
		// Drivers stringify everything via fmt.Sprintf("%v", v) before
		// the sanitiser sees it; we mirror that here so the redactor
		// sees the same surface area. Already-string values bypass
		// the Sprintf to keep the common path allocation-free.
		switch t := v.(type) {
		case string:
			out[i] = r.redactor.Redact(t)
		case fmt.Stringer:
			out[i] = r.redactor.Redact(t.String())
		default:
			out[i] = r.redactor.Redact(fmt.Sprintf("%v", v))
		}
	}
	return rMsg, out
}

func (r *redactingLogger) Debug(msg string, kvs ...any) {
	m, k := r.redact(msg, kvs)
	r.inner.Debug(m, k...)
}

func (r *redactingLogger) Info(msg string, kvs ...any) {
	m, k := r.redact(msg, kvs)
	r.inner.Info(m, k...)
}

func (r *redactingLogger) Warn(msg string, kvs ...any) {
	m, k := r.redact(msg, kvs)
	r.inner.Warn(m, k...)
}

func (r *redactingLogger) Error(msg string, kvs ...any) {
	m, k := r.redact(msg, kvs)
	r.inner.Error(m, k...)
}

func (r *redactingLogger) Fatal(msg string, kvs ...any) {
	m, k := r.redact(msg, kvs)
	r.inner.Fatal(m, k...)
}

// Shutdown forwards to the wrapped logger when it implements the
// optional Shutdowner interface. Without this the framework would
// leak file handles whenever a redacting wrapper was the outermost
// reference to a *FileLogger or *StackLogger.
func (r *redactingLogger) Shutdown(ctx context.Context) error {
	if s, ok := r.inner.(Shutdowner); ok {
		return s.Shutdown(ctx)
	}
	return nil
}
