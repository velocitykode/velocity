package log

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHeaderRedactor_StripsSensitiveValues confirms the canonical
// sensitive HTTP headers have their value half replaced with
// [REDACTED] while the header name is preserved so operators can
// still tell which header fired.
func TestHeaderRedactor_StripsSensitiveValues(t *testing.T) {
	r := HeaderRedactor()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "Authorization bearer token",
			in:   "Authorization: Bearer abc123secret",
			want: "Authorization: [REDACTED]",
		},
		{
			name: "Cookie header",
			in:   "Cookie: session=secret_value",
			want: "Cookie: [REDACTED]",
		},
		{
			name: "Set-Cookie header",
			in:   "Set-Cookie: sid=abc; HttpOnly",
			want: "Set-Cookie: [REDACTED]; HttpOnly",
		},
		{
			name: "X-API-Key header",
			in:   "X-API-Key: keyabcdef",
			want: "X-API-Key: [REDACTED]",
		},
		{
			name: "X-Auth-Token header",
			in:   "X-Auth-Token: deadbeef",
			want: "X-Auth-Token: [REDACTED]",
		},
		{
			name: "Proxy-Authorization header",
			in:   "Proxy-Authorization: Basic ZXZpbA==",
			want: "Proxy-Authorization: [REDACTED]",
		},
		{
			name: "case insensitive",
			in:   "authorization: Bearer xyz",
			want: "authorization: [REDACTED]",
		},
		{
			name: "equals separator (URL-encoded form)",
			in:   "Cookie=session=value",
			want: "Cookie= [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.in)
			if got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestHeaderRedactor_LeavesUnrelatedHeadersAlone is the negative case
// for the header redactor: a header that is NOT in the sensitive list
// must pass through verbatim so operators do not lose ordinary debug
// context like User-Agent or Content-Type.
func TestHeaderRedactor_LeavesUnrelatedHeadersAlone(t *testing.T) {
	r := HeaderRedactor()
	cases := []string{
		"User-Agent: curl/8.4.0",
		"Content-Type: application/json",
		"Accept: */*",
		"Host: example.com",
		"plain text with no header at all",
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestHeaderRedactor_CookieMultiPair_FullValueRedacted is the M-38
// regression pin. Pre-fix the value-half regex stopped at the first
// ';' so a Cookie header with multiple cookie pairs leaked everything
// after the first pair into the log file. The fix swaps the Cookie
// header onto the "redact to end-of-line" pattern so every pair after
// the first ';' is masked too.
func TestHeaderRedactor_CookieMultiPair_FullValueRedacted(t *testing.T) {
	r := HeaderRedactor()
	in := "Cookie: session=a; csrf=b; remember=c"
	got := r.Redact(in)

	// The whole value half must collapse to [REDACTED]; no cookie
	// name=value pair after the first ';' may survive.
	if got != "Cookie: [REDACTED]" {
		t.Errorf("Redact(%q) = %q, want %q", in, got, "Cookie: [REDACTED]")
	}
	for _, leaked := range []string{"session=a", "csrf=b", "remember=c"} {
		if strings.Contains(got, leaked) {
			t.Errorf("Redact leaked cookie pair %q in output: %s", leaked, got)
		}
	}
}

// TestHeaderRedactor_CookieMultiPair_OwnLine_NoLeak is the multi-line
// shape of the same M-38 regression: a Cookie header that occupies
// its own log line must have every cookie pair redacted, not just the
// first. Mirrors how production loggers print headers (one per line).
func TestHeaderRedactor_CookieMultiPair_OwnLine_NoLeak(t *testing.T) {
	r := HeaderRedactor()
	in := "request:\nCookie: session=abc123; XSRF-TOKEN=xyz789; remember_me=def456\nUser-Agent: curl/8.4.0"
	got := r.Redact(in)

	// Surrounding context (User-Agent) survives, but every cookie pair
	// in the Cookie header is gone.
	if !strings.Contains(got, "User-Agent: curl/8.4.0") {
		t.Errorf("Redact wiped unrelated header context: %s", got)
	}
	for _, leaked := range []string{"session=abc123", "XSRF-TOKEN=xyz789", "remember_me=def456", "abc123", "xyz789", "def456"} {
		if strings.Contains(got, leaked) {
			t.Errorf("Redact leaked cookie material %q in multi-line output:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "Cookie: [REDACTED]") {
		t.Errorf("Redact did not insert [REDACTED] for Cookie line:\n%s", got)
	}
}

// TestHeaderRedactor_Authorization_BearerStillRedacted confirms the
// M-38 fix did not regress the single-value Authorization header.
// Bearer / Basic / token values have no internal ';' so the prior
// behaviour and the new behaviour both end at CR / LF / end-of-input;
// the test is here to lock that explicitly.
func TestHeaderRedactor_Authorization_BearerStillRedacted(t *testing.T) {
	r := HeaderRedactor()
	in := "Authorization: Bearer foo.bar.baz"
	got := r.Redact(in)
	if got != "Authorization: [REDACTED]" {
		t.Errorf("Redact(%q) = %q, want %q", in, got, "Authorization: [REDACTED]")
	}
	if strings.Contains(got, "foo.bar.baz") {
		t.Errorf("Redact leaked bearer token: %s", got)
	}
}

// TestHeaderRedactor_SetCookie_ApproachBPreservesAttributes pins the
// design choice for Set-Cookie: redact the cookie name=value pair
// before the first ';' and PRESERVE the security attributes after
// (Path, HttpOnly, Secure, SameSite, Domain, Max-Age, Expires).
//
// Rationale: those attributes are public metadata that operators
// scrape from logs to verify cookie-security posture in production
// ("is my session cookie actually HttpOnly+Secure+SameSite=Lax?").
// The credential sits before the ';'; the attributes after are not
// secrets. Approach (A) "redact entire value" would erase real
// observability signal for zero security gain. See
// pairValueSensitiveHeaders / HeaderRedactor godoc.
func TestHeaderRedactor_SetCookie_ApproachBPreservesAttributes(t *testing.T) {
	r := HeaderRedactor()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "session cookie with HttpOnly",
			in:   "Set-Cookie: session=abc; HttpOnly",
			want: "Set-Cookie: [REDACTED]; HttpOnly",
		},
		{
			name: "session cookie with full attribute set",
			in:   "Set-Cookie: session=abc; Path=/; HttpOnly; Secure; SameSite=Lax",
			want: "Set-Cookie: [REDACTED]; Path=/; HttpOnly; Secure; SameSite=Lax",
		},
		{
			name: "remember-me cookie with Max-Age",
			in:   "Set-Cookie: remember_me=longsecret; Max-Age=2592000; Path=/; HttpOnly",
			want: "Set-Cookie: [REDACTED]; Max-Age=2592000; Path=/; HttpOnly",
		},
		{
			name: "cookie with Domain",
			in:   "Set-Cookie: id=value; Domain=.example.com; Path=/",
			want: "Set-Cookie: [REDACTED]; Domain=.example.com; Path=/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.in)
			if got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestHeaderRedactor_BenignSemicolonElsewhereUnaffected guards
// against the M-38 fix over-reaching: a benign log line that happens
// to contain ';' chars (SQL fragments, code snippets, paths) must not
// be touched if no sensitive header name is present.
func TestHeaderRedactor_BenignSemicolonElsewhereUnaffected(t *testing.T) {
	r := HeaderRedactor()
	cases := []string{
		"SELECT 1; UPDATE users SET name='x'; DELETE FROM sessions;",
		"PATH=/usr/local/bin:/usr/bin; export PATH",
		"params: a=1; b=2; c=3 (no header context)",
		"line with semicolons; nothing sensitive here; really; truly;",
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestJWTRedactor_ReplacesValidTriple verifies the 3-segment base64url
// JWT shape is masked. The "eyJ" prefix is required on the first two
// segments so the regex does not false-fire on arbitrary dotted
// identifiers.
func TestJWTRedactor_ReplacesValidTriple(t *testing.T) {
	r := JWTRedactor()
	// Real-shape JWT: header.payload.signature, each base64url.
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjMiLCJuYW1lIjoiSm9lIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	in := "auth failed for token=" + jwt + " from 10.0.0.1"
	out := r.Redact(in)
	if strings.Contains(out, jwt) {
		t.Errorf("Redact left raw JWT in output:\n%s", out)
	}
	if !strings.Contains(out, "[JWT]") {
		t.Errorf("Redact did not insert [JWT] marker:\n%s", out)
	}
	if !strings.Contains(out, "10.0.0.1") {
		t.Errorf("Redact wiped surrounding context:\n%s", out)
	}
}

// TestJWTRedactor_IgnoresNonJWTDottedIDs is the negative case: dotted
// version numbers, file paths, and arbitrary base64url-looking
// strings without the required "eyJ" header prefix must NOT match.
func TestJWTRedactor_IgnoresNonJWTDottedIDs(t *testing.T) {
	r := JWTRedactor()
	cases := []string{
		"version 1.2.3",
		"path/to/file.tar.gz",
		"deadbeef.cafebabe.fa11dead",           // hex but missing eyJ prefix
		"AAAA.BBBB.CCCC",                       // base64url but missing eyJ
		"https://example.com/path/to.resource", // dotted URL
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestPANRedactor_ReplacesLuhnValid confirms common test card numbers
// (all Luhn-valid by spec) are masked. Both contiguous and separated
// forms are covered.
func TestPANRedactor_ReplacesLuhnValid(t *testing.T) {
	r := PANRedactor()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "Visa contiguous",
			in:   "card=4111111111111111 saved",
			want: "card=[CARD] saved",
		},
		{
			name: "Visa space separated",
			in:   "card=4111 1111 1111 1111 saved",
			want: "card=[CARD] saved",
		},
		{
			name: "Visa dash separated",
			in:   "card=4111-1111-1111-1111 saved",
			want: "card=[CARD] saved",
		},
		{
			name: "Mastercard",
			in:   "pan=5555555555554444",
			want: "pan=[CARD]",
		},
		{
			name: "Amex 15-digit",
			in:   "amex=378282246310005 end",
			want: "amex=[CARD] end",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.in)
			if got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPANRedactor_IgnoresNonLuhn ensures random 13-19 digit runs that
// fail the Luhn check pass through. This is the false-positive guard:
// order IDs, sequence numbers, and timestamps look like PANs by
// shape but should not be wiped.
func TestPANRedactor_IgnoresNonLuhn(t *testing.T) {
	r := PANRedactor()
	cases := []string{
		"order_id=1234567890123",     // 13 digits, Luhn-invalid
		"timestamp=1717000000000000", // 16 digits, Luhn-invalid
		"id=9999999999999999",        // 16 nines, Luhn-invalid (sum 144)
		"sequence 1111111111111110",  // 16 digits, Luhn-invalid
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged (non-Luhn passthrough)", in, got)
		}
	}
}

// TestPANRedactor_ShortAndLongRunsIgnored verifies the length cap.
// PANs are 13-19 digits; anything outside that band cannot be a real
// PAN per ISO/IEC 7812 and must pass through.
func TestPANRedactor_ShortAndLongRunsIgnored(t *testing.T) {
	r := PANRedactor()
	cases := []string{
		"short=123456789012",        // 12 digits
		"long=12345678901234567890", // 20 digits
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged (out-of-range)", in, got)
		}
	}
}

// TestEmailRedactor_ReplacesValidAddresses confirms the opt-in email
// redactor masks RFC-shape addresses while leaving the surrounding
// log line intact.
func TestEmailRedactor_ReplacesValidAddresses(t *testing.T) {
	r := EmailRedactor()
	tests := []struct {
		in   string
		want string
	}{
		{"login from user@example.com ok", "login from [EMAIL] ok"},
		{"a.b+c@sub.example.co.uk", "[EMAIL]"},
		{"two: alice@x.com bob@y.com", "two: [EMAIL] [EMAIL]"},
	}
	for _, tt := range tests {
		got := r.Redact(tt.in)
		if got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestEmailRedactor_IgnoresNonAddresses guards against false positives
// on strings that contain "@" but are not email addresses (mentions,
// path fragments, scoped package names).
func TestEmailRedactor_IgnoresNonAddresses(t *testing.T) {
	r := EmailRedactor()
	cases := []string{
		"@channel ping",     // Slack-style mention
		"@scope/package",    // npm scope
		"plain text",        // no @ at all
		"trailing@",         // local but no domain
		"@example.com only", // domain but no local
	}
	for _, in := range cases {
		got := r.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestEmailRedactor_OffByDefault verifies BuildDefaultRedactors does
// NOT include EmailRedactor when LOG_REDACT_EMAILS is unset. Email
// addresses are often the only useful debug breadcrumb, so the
// framework defaults to keeping them.
func TestEmailRedactor_OffByDefault(t *testing.T) {
	t.Setenv("LOG_REDACT_EMAILS", "")
	resetDefaultRedactorCache(t)

	chain := BuildDefaultRedactors()
	in := "login from alice@example.com ok"
	got := chain.Redact(in)
	if !strings.Contains(got, "alice@example.com") {
		t.Errorf("default chain redacted email when LOG_REDACT_EMAILS unset:\n%s", got)
	}
}

// TestEmailRedactor_OptInViaEnv flips LOG_REDACT_EMAILS=true and
// confirms BuildDefaultRedactors picks the email rule up. This is
// the documented escape hatch for compliance regimes that require
// pseudonymisation by default.
func TestEmailRedactor_OptInViaEnv(t *testing.T) {
	t.Setenv("LOG_REDACT_EMAILS", "true")
	resetDefaultRedactorCache(t)

	chain := BuildDefaultRedactors()
	in := "login from alice@example.com ok"
	got := chain.Redact(in)
	if strings.Contains(got, "alice@example.com") {
		t.Errorf("env opt-in did not redact email:\n%s", got)
	}
	if !strings.Contains(got, "[EMAIL]") {
		t.Errorf("env opt-in did not insert [EMAIL] marker:\n%s", got)
	}
}

// TestChain_ComposesInOrder pins the documented ordering contract:
// earlier redactors mask substrings later redactors might otherwise
// see. Here, masking the Authorization value to [REDACTED] before the
// JWT rule runs hides the embedded token so the JWT rule does NOT
// also fire (the outer mask is already complete).
func TestChain_ComposesInOrder(t *testing.T) {
	chain := Chain(HeaderRedactor(), JWTRedactor())
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature_value"
	in := "Authorization: Bearer " + jwt
	out := chain.Redact(in)

	if strings.Contains(out, jwt) {
		t.Errorf("chain left raw JWT in output:\n%s", out)
	}
	if strings.Contains(out, "[JWT]") {
		t.Errorf("chain ran JWT redactor on already-masked header value:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("chain did not insert [REDACTED] marker:\n%s", out)
	}
}

// TestChain_AppliesAllStages confirms the chain runs every stage when
// matches sit on independent log lines. The HeaderRedactor consumes
// everything up to end-of-line for Authorization / Cookie / etc, so
// we put each kind of secret on its own line; in a real multi-line
// log dump this is the natural shape.
func TestChain_AppliesAllStages(t *testing.T) {
	chain := Chain(HeaderRedactor(), JWTRedactor(), PANRedactor())
	jwt := "eyJ0.eyJ1.sig_value"
	in := "Authorization: Bearer abc\ntoken " + jwt + "\ncard 4111111111111111"
	out := chain.Redact(in)

	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("missing [REDACTED]:\n%s", out)
	}
	if !strings.Contains(out, "[JWT]") {
		t.Errorf("missing [JWT]:\n%s", out)
	}
	if !strings.Contains(out, "[CARD]") {
		t.Errorf("missing [CARD]:\n%s", out)
	}
}

// TestChain_EmptyIsIdentity covers the degenerate case where no
// redactors are configured: the chain must be the identity function,
// not a panic and not an empty string.
func TestChain_EmptyIsIdentity(t *testing.T) {
	chain := Chain()
	in := "Authorization: Bearer secret"
	if got := chain.Redact(in); got != in {
		t.Errorf("empty chain altered input: got %q, want %q", got, in)
	}
}

// TestChain_ReDoSSmoke runs the entire chain against a pathological
// input shaped to trip the classic ReDoS hot-spots (email "@" runs,
// long digit runs for PAN, long base64url tails for JWT) and
// confirms it completes within a generous bound. Go's regexp engine
// is RE2-backed (no backreferences, no nested unbounded backtracking)
// so this is a regression guard: if a future redactor swaps in a
// non-RE2 implementation with catastrophic backtracking, this test
// times out instead of returning silently.
//
// Bound is set to 15s rather than 1s so the test passes reliably
// under -race on slow CI runners (which can add 10-15x overhead on
// top of a cold GitHub Actions VM); a true exponential-time regex
// on this input would not finish in minutes, so 15s still detects
// catastrophic backtracking with margin to spare.
func TestChain_ReDoSSmoke(t *testing.T) {
	chain := Chain(HeaderRedactor(), JWTRedactor(), PANRedactor(), EmailRedactor())

	// Build a pathological input: 32 KiB of "a@" repetitions (the
	// classic email-regex ReDoS shape) plus a long contiguous digit
	// run (PAN regex pathological shape) plus a long base64url tail
	// (JWT regex pathological shape).
	var b strings.Builder
	b.Grow(96 * 1024)
	for b.Len() < 32*1024 {
		b.WriteString("a@")
	}
	for b.Len() < 64*1024 {
		b.WriteByte('1')
	}
	for b.Len() < 96*1024 {
		b.WriteString("eyJ")
	}
	b.WriteString(".eyJ.")
	in := b.String()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		_ = chain.Redact(in)
		close(done)
	}()
	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > 15*time.Second {
			t.Errorf("chain on ~96 KiB input took %v, want <15s", elapsed)
		}
		t.Logf("chain completed in %v", elapsed)
	case <-time.After(30 * time.Second):
		t.Fatal("chain did not complete within 30s on ~96 KiB input (possible ReDoS)")
	}
}

// TestWithRedactors_WrapsMsgAndKvs confirms the logger wrapper rewrites
// both the message and every kv argument before delegating to the
// inner driver. Uses a capturing logger to read back what the inner
// driver would have seen.
func TestWithRedactors_WrapsMsgAndKvs(t *testing.T) {
	cap := &capturingLogger{}
	wrapped := WithRedactors(cap, HeaderRedactor(), JWTRedactor())

	wrapped.Info("Authorization: Bearer secret", "token", "eyJ0.eyJ1.sig_value", "user_id", 42)

	if cap.msg != "Authorization: [REDACTED]" {
		t.Errorf("msg not redacted: %q", cap.msg)
	}
	if len(cap.kvs) != 4 {
		t.Fatalf("expected 4 kv args, got %d: %v", len(cap.kvs), cap.kvs)
	}
	if got := cap.kvs[1].(string); got != "[JWT]" {
		t.Errorf("kv value not redacted: %q", got)
	}
	// Non-sensitive kv must pass through (stringified).
	if got := cap.kvs[3].(string); got != "42" {
		t.Errorf("non-sensitive kv mangled: %q", got)
	}
}

// TestWithRedactors_ZeroIsNoop confirms passing zero redactors returns
// the inner logger unwrapped so the common path stays allocation-free.
func TestWithRedactors_ZeroIsNoop(t *testing.T) {
	cap := &capturingLogger{}
	got := WithRedactors(cap)
	if got != Logger(cap) {
		t.Errorf("zero-redactor wrap should return inner: got %T, want %T", got, cap)
	}
}

// TestWithRedactors_ForwardsShutdown ensures the redactingLogger
// delegates Shutdown to the wrapped driver when the driver supports
// it. Without this, a *FileLogger wrapped in redaction would leak
// file handles on app shutdown.
func TestWithRedactors_ForwardsShutdown(t *testing.T) {
	shut := &shutdownCapture{}
	wrapped := WithRedactors(shut, HeaderRedactor())
	s, ok := wrapped.(Shutdowner)
	if !ok {
		t.Fatal("redactingLogger does not implement Shutdowner")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if !shut.called {
		t.Error("Shutdown not forwarded to inner logger")
	}
}

// TestRedactorFunc_SatisfiesInterface is a compile-time pin via
// runtime use: RedactorFunc must satisfy Redactor so callers can
// register an inline closure without declaring a struct.
func TestRedactorFunc_SatisfiesInterface(t *testing.T) {
	var r Redactor = RedactorFunc(func(s string) string {
		return strings.ReplaceAll(s, "foo", "bar")
	})
	if got := r.Redact("foofoo"); got != "barbar" {
		t.Errorf("RedactorFunc.Redact = %q, want %q", got, "barbar")
	}
}

// TestDefaultRedactors_CachedAcrossCalls verifies BuildDefaultRedactors
// returns the same chain instance on repeated calls (sync.Once cache)
// so consumers can hold a reference without paying for chain
// composition on every log line.
func TestDefaultRedactors_CachedAcrossCalls(t *testing.T) {
	resetDefaultRedactorCache(t)
	a := BuildDefaultRedactors()
	b := BuildDefaultRedactors()
	// Compare by Redact behaviour rather than by pointer (RedactorFunc
	// is not comparable across closure captures).
	if a.Redact("x") != b.Redact("x") {
		t.Errorf("BuildDefaultRedactors returned different chains across calls")
	}
}

// TestFactoryWiring_RedactOptInViaConfig confirms a file-driver config
// with "redact": true returns a wrapped logger, so a stack channel
// can flip redaction on per-target without rebuilding the factory.
func TestFactoryWiring_RedactOptInViaConfig(t *testing.T) {
	t.Setenv("LOG_REDACT", "")
	resetDefaultRedactorCache(t)

	tempDir := t.TempDir()
	logger, err := NewLogger(LogConfig{
		Driver: "file",
		Config: map[string]any{
			"path":   tempDir,
			"redact": true,
		},
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	logger.Info("Authorization: Bearer abc123secret", "card", "4111111111111111")

	if s, ok := logger.(Shutdowner); ok {
		_ = s.Shutdown(context.Background())
	}

	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one log file, got %d", len(files))
	}
	content, err := os.ReadFile(tempDir + "/" + files[0].Name())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	body := string(content)
	if strings.Contains(body, "abc123secret") {
		t.Errorf("config opt-in did not redact secret:\n%s", body)
	}
	if strings.Contains(body, "4111111111111111") {
		t.Errorf("config opt-in did not redact PAN:\n%s", body)
	}
}

// TestFactoryWiring_RedactOptInViaEnv exercises the LOG_REDACT=true
// env hook: a stock file driver config without any explicit redact
// flag must still redact when the env flag is set.
func TestFactoryWiring_RedactOptInViaEnv(t *testing.T) {
	t.Setenv("LOG_REDACT", "true")
	resetDefaultRedactorCache(t)

	tempDir := t.TempDir()
	logger, err := NewLogger(LogConfig{
		Driver: "file",
		Config: map[string]any{"path": tempDir},
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	logger.Info("Authorization: Bearer envtoken")
	if s, ok := logger.(Shutdowner); ok {
		_ = s.Shutdown(context.Background())
	}

	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	content, err := os.ReadFile(tempDir + "/" + files[0].Name())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if strings.Contains(string(content), "envtoken") {
		t.Errorf("env opt-in did not redact:\n%s", content)
	}
}

// TestFactoryWiring_DefaultIsNoRedaction confirms the framework
// defaults to NO redaction. Operators opt in explicitly via config or
// env; we do not silently strip data that might be needed for debug.
func TestFactoryWiring_DefaultIsNoRedaction(t *testing.T) {
	t.Setenv("LOG_REDACT", "")
	t.Setenv("LOG_REDACT_EMAILS", "")
	resetDefaultRedactorCache(t)

	tempDir := t.TempDir()
	logger, err := NewLogger(LogConfig{
		Driver: "file",
		Config: map[string]any{"path": tempDir},
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	logger.Info("Authorization: Bearer keepme")
	if s, ok := logger.(Shutdowner); ok {
		_ = s.Shutdown(context.Background())
	}

	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	content, err := os.ReadFile(tempDir + "/" + files[0].Name())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "keepme") {
		t.Errorf("default config unexpectedly redacted:\n%s", content)
	}
}

// resetDefaultRedactorCache clears the sync.Once cache between tests
// so each test sees BuildDefaultRedactors reflect the current env.
// Direct package access is fine - tests live in the same package.
func resetDefaultRedactorCache(t *testing.T) {
	t.Helper()
	defaultRedactorOnce = sync.Once{}
	defaultRedactorCache = nil
}

// capturingLogger records the most recent call to any level so tests
// can assert on the bytes the inner driver would have written.
type capturingLogger struct {
	level string
	msg   string
	kvs   []any
}

func (c *capturingLogger) Debug(msg string, kvs ...any) { c.level, c.msg, c.kvs = "DEBUG", msg, kvs }
func (c *capturingLogger) Info(msg string, kvs ...any)  { c.level, c.msg, c.kvs = "INFO", msg, kvs }
func (c *capturingLogger) Warn(msg string, kvs ...any)  { c.level, c.msg, c.kvs = "WARN", msg, kvs }
func (c *capturingLogger) Error(msg string, kvs ...any) { c.level, c.msg, c.kvs = "ERROR", msg, kvs }
func (c *capturingLogger) Fatal(msg string, kvs ...any) { c.level, c.msg, c.kvs = "FATAL", msg, kvs }

// shutdownCapture is a Logger + Shutdowner used to assert the
// redacting wrapper forwards Shutdown calls.
type shutdownCapture struct {
	capturingLogger
	called bool
}

func (s *shutdownCapture) Shutdown(_ context.Context) error {
	s.called = true
	return nil
}

// leveledCapturingLogger is a capturingLogger that also exposes a Level so
// the redacting wrapper engages its below-level gate. The embedded
// capturingLogger does NOT itself gate by level (it records every call),
// so any record that fails to reach it was dropped by the wrapper, not the
// inner logger - exactly what the gate tests want to observe.
type leveledCapturingLogger struct {
	capturingLogger
	lvl int
}

func (l *leveledCapturingLogger) Level() int { return l.lvl }

// countingRedactor records how many times Redact was invoked so a test can
// prove the wrapper performed zero redaction work for a gated record. It
// otherwise returns the input untouched.
type countingRedactor struct{ calls int }

func (c *countingRedactor) Redact(s string) string {
	c.calls++
	return s
}

// TestRedactingLogger_BelowLevelSkipsRedaction proves a Debug call against
// a wrapper whose inner logger is at INFO performs no redaction work and
// does not reach the inner logger.
func TestRedactingLogger_BelowLevelSkipsRedaction(t *testing.T) {
	inner := &leveledCapturingLogger{lvl: int(INFO)}
	counter := &countingRedactor{}
	wrapped := WithRedactors(inner, counter)

	wrapped.Debug("Authorization: Bearer secret", "token", "eyJ0.eyJ1.sig")

	if counter.calls != 0 {
		t.Errorf("Debug-at-INFO ran %d redactions, want 0", counter.calls)
	}
	if inner.level != "" {
		t.Errorf("suppressed Debug reached inner logger as %q", inner.level)
	}
}

// TestRedactingLogger_AtOrAboveLevelByteIdentical confirms a record that
// passes the level filter is redacted byte-for-byte identically to the
// un-gated wrapper, so the optimisation never changes emitted output.
func TestRedactingLogger_AtOrAboveLevelByteIdentical(t *testing.T) {
	const msg = "Authorization: Bearer secret"
	args := []any{"token", "eyJ0.eyJ1.sig_value", "user_id", 42}

	// Gated wrapper: inner logger sits at INFO.
	gatedInner := &leveledCapturingLogger{lvl: int(INFO)}
	gated := WithRedactors(gatedInner, HeaderRedactor(), JWTRedactor())

	// Reference wrapper: plain capturingLogger exposes no level, so gating
	// is disabled and redaction always runs (the pre-change behaviour).
	refInner := &capturingLogger{}
	ref := WithRedactors(refInner, HeaderRedactor(), JWTRedactor())

	gated.Info(msg, args...)
	ref.Info(msg, args...)

	if gatedInner.msg != refInner.msg {
		t.Errorf("msg differs: gated %q vs ref %q", gatedInner.msg, refInner.msg)
	}
	if len(gatedInner.kvs) != len(refInner.kvs) {
		t.Fatalf("kv count differs: gated %d vs ref %d", len(gatedInner.kvs), len(refInner.kvs))
	}
	for i := range gatedInner.kvs {
		if gatedInner.kvs[i] != refInner.kvs[i] {
			t.Errorf("kv[%d] differs: gated %v vs ref %v", i, gatedInner.kvs[i], refInner.kvs[i])
		}
	}
}

// TestRedactingLogger_FatalAlwaysRedacts confirms Fatal is never gated:
// even when the inner level suppresses every lower severity, Fatal still
// reaches the inner logger with its content redacted.
func TestRedactingLogger_FatalAlwaysRedacts(t *testing.T) {
	inner := &leveledCapturingLogger{lvl: int(FATAL)}
	wrapped := WithRedactors(inner, HeaderRedactor())

	wrapped.Fatal("Authorization: Bearer secret")

	if inner.level != "FATAL" {
		t.Fatalf("Fatal did not reach inner logger (level %q)", inner.level)
	}
	if inner.msg != "Authorization: [REDACTED]" {
		t.Errorf("Fatal msg not redacted: %q", inner.msg)
	}
}

// TestRedactingLogger_NoLevelGatingDisabled confirms an inner logger that
// does not expose a level always runs redaction (no gating), preserving
// the original behaviour for null / third-party drivers.
func TestRedactingLogger_NoLevelGatingDisabled(t *testing.T) {
	inner := &capturingLogger{}
	counter := &countingRedactor{}
	wrapped := WithRedactors(inner, counter)

	wrapped.Debug("anything")

	if counter.calls == 0 {
		t.Error("Debug ran 0 redactions; gating should be disabled without a level source")
	}
}

// BenchmarkRedactingLogger_BelowLevel measures the cost of a Debug call
// that the inner INFO-level logger would discard. With the level gate it
// should perform no redaction (no regex passes, no per-kv Sprintf).
func BenchmarkRedactingLogger_BelowLevel(b *testing.B) {
	inner := &leveledCapturingLogger{lvl: int(INFO)}
	wrapped := WithRedactors(inner, BuildDefaultRedactors())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wrapped.Debug("Authorization: Bearer secret", "token", "eyJ0.eyJ1.sig", "n", i)
	}
}

// TestRedactingLogger_StackBelowLevelSkipsRedaction proves the below-level
// gate engages for a *StackLogger whose children all expose a level. A Debug
// call against a stack of INFO-level children performs no redaction and
// reaches none of the children.
func TestRedactingLogger_StackBelowLevelSkipsRedaction(t *testing.T) {
	c1 := &leveledCapturingLogger{lvl: int(INFO)}
	c2 := &leveledCapturingLogger{lvl: int(INFO)}
	stack := NewStackLogger(c1, c2)
	counter := &countingRedactor{}
	wrapped := WithRedactors(stack, counter)

	wrapped.Debug("Authorization: Bearer secret", "token", "eyJ0.eyJ1.sig")

	if counter.calls != 0 {
		t.Errorf("Debug-at-INFO stack ran %d redactions, want 0", counter.calls)
	}
	if c1.level != "" || c2.level != "" {
		t.Errorf("suppressed Debug reached stack children: %q / %q", c1.level, c2.level)
	}
}

// TestRedactingLogger_StackLevelIsMinChild confirms the stack threshold is the
// minimum of its children: a record below the lowest child level is gated,
// while one at that level still fans out.
func TestRedactingLogger_StackLevelIsMinChild(t *testing.T) {
	info := &leveledCapturingLogger{lvl: int(INFO)}
	warn := &leveledCapturingLogger{lvl: int(WARN)}
	stack := NewStackLogger(info, warn)
	counter := &countingRedactor{}
	wrapped := WithRedactors(stack, counter)

	// Debug is below INFO (the min child level) - gated.
	wrapped.Debug("secret")
	if counter.calls != 0 {
		t.Errorf("Debug below min child level ran %d redactions, want 0", counter.calls)
	}

	// Info is at the min child level - redaction runs and the INFO child sees it.
	wrapped.Info("secret")
	if counter.calls == 0 {
		t.Error("Info at min child level ran 0 redactions; gate should pass it through")
	}
	if info.level != "INFO" {
		t.Errorf("Info did not reach min-level child (level %q)", info.level)
	}
}

// TestRedactingLogger_StackUnknownChildDisablesGate confirms that a stack with
// even one child that does not expose a level disables gating entirely, so
// redaction always runs (the conservative always-redact path).
func TestRedactingLogger_StackUnknownChildDisablesGate(t *testing.T) {
	leveled := &leveledCapturingLogger{lvl: int(INFO)}
	plain := &capturingLogger{} // no Level() method
	stack := NewStackLogger(leveled, plain)
	counter := &countingRedactor{}
	wrapped := WithRedactors(stack, counter)

	wrapped.Debug("anything")

	if counter.calls == 0 {
		t.Error("Debug ran 0 redactions; an unknown-level child must disable gating")
	}
}

// BenchmarkRedactingLogger_StackBelowLevel measures a Debug call against a
// stack of INFO-level children. With the stack-wide level gate it should
// perform no redaction (no regex passes, no per-kv Sprintf).
func BenchmarkRedactingLogger_StackBelowLevel(b *testing.B) {
	stack := NewStackLogger(
		&leveledCapturingLogger{lvl: int(INFO)},
		&leveledCapturingLogger{lvl: int(INFO)},
	)
	wrapped := WithRedactors(stack, BuildDefaultRedactors())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wrapped.Debug("Authorization: Bearer secret", "token", "eyJ0.eyJ1.sig", "n", i)
	}
}
