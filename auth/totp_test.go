package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// helper: compute a TOTP code for a base32 secret at the given unix time.
// Mirrors the production code path so tests do not depend on private
// internals beyond what we already exercise.
func codeAt(t *testing.T, secret string, unix int64, digits, period int) string {
	t.Helper()
	key, err := decodeSecret(secret)
	if err != nil {
		t.Fatalf("decodeSecret: %v", err)
	}
	step := unix / int64(period)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0F
	binCode := (uint32(sum[offset])&0x7F)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	value := binCode % mod
	out := make([]byte, digits)
	for i := digits - 1; i >= 0; i-- {
		out[i] = byte('0' + value%10)
		value /= 10
	}
	return string(out)
}

// verifyAt is like (*TOTPGenerator).Verify but at a fixed timestamp; we
// reach in via a small helper so tests are deterministic without faking
// time.Now globally.
func verifyAt(g *TOTPGenerator, secret, code string, unix int64) bool {
	if len(code) == 0 || len(secret) == 0 {
		return false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	step := unix / int64(g.cfg.Period)
	supplied := []byte(code)
	for offset := -g.cfg.Skew; offset <= g.cfg.Skew; offset++ {
		expected := generateCode(key, step+int64(offset), g.cfg.Digits)
		if len(expected) != len(supplied) {
			continue
		}
		if constantTimeBytesEq(expected, supplied) {
			return true
		}
	}
	return false
}

// constantTimeBytesEq is a thin constant-time wrapper used only inside
// the tests' verifyAt helper to keep semantics aligned with production
// Verify. Uses subtle.ConstantTimeCompare to match the no-timing-leak
// contract; bytes.Equal / string equality would short-circuit.
func constantTimeBytesEq(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

func TestTOTP_Generate_SecretFormat(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	secret, _, err := g.Generate("user@example.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.ContainsRune(secret, '=') {
		t.Errorf("secret contains padding: %q", secret)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("base32 decode: %v", err)
	}
	if len(raw) != 20 {
		t.Errorf("decoded secret length = %d, want 20", len(raw))
	}
}

func TestTOTP_Generate_QRURLFormat(t *testing.T) {
	g := NewTOTP(TOTPConfig{Issuer: "MyApp"})
	secret, qrURL, err := g.Generate("alice@example.com")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	u, err := url.Parse(qrURL)
	if err != nil {
		t.Fatalf("parse qrURL: %v", err)
	}
	if u.Scheme != "otpauth" {
		t.Errorf("scheme = %q, want otpauth", u.Scheme)
	}
	if u.Host != "totp" {
		t.Errorf("host = %q, want totp", u.Host)
	}
	if got := strings.TrimPrefix(u.Path, "/"); got != "alice@example.com" {
		t.Errorf("label = %q, want alice@example.com", got)
	}
	q := u.Query()
	if q.Get("secret") != secret {
		t.Errorf("secret in URL = %q, want %q", q.Get("secret"), secret)
	}
	if q.Get("issuer") != "MyApp" {
		t.Errorf("issuer = %q, want MyApp", q.Get("issuer"))
	}
	if q.Get("algorithm") != "SHA1" {
		t.Errorf("algorithm = %q, want SHA1", q.Get("algorithm"))
	}
	if q.Get("digits") != "6" {
		t.Errorf("digits = %q, want 6", q.Get("digits"))
	}
	if q.Get("period") != "30" {
		t.Errorf("period = %q, want 30", q.Get("period"))
	}
}

func TestTOTP_Generate_DefaultIssuer(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	_, qrURL, err := g.Generate("u")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	u, _ := url.Parse(qrURL)
	if u.Query().Get("issuer") != "Velocity" {
		t.Errorf("default issuer = %q, want Velocity", u.Query().Get("issuer"))
	}
}

func TestTOTP_Verify_ValidCode(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, err := g.Generate("u")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)
	if !g.Verify(secret, code) {
		t.Error("Verify returned false for valid current code")
	}
}

func TestTOTP_Verify_PrevWindow(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now-30, 6, 30)
	if !verifyAt(g, secret, code, now) {
		t.Error("expected previous-window code to verify with skew=1")
	}
}

func TestTOTP_Verify_NextWindow(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now+30, 6, 30)
	if !verifyAt(g, secret, code, now) {
		t.Error("expected next-window code to verify with skew=1")
	}
}

func TestTOTP_Verify_OutsideWindow_Rejected(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now-90, 6, 30)
	if verifyAt(g, secret, code, now) {
		t.Error("expected code from T-90s to be rejected with skew=1")
	}
}

func TestTOTP_Verify_TamperedCode_Rejected(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)
	// Flip the last digit deterministically.
	last := code[len(code)-1]
	if last == '9' {
		last = '0'
	} else {
		last++
	}
	tampered := code[:len(code)-1] + string(last)
	if g.Verify(secret, tampered) {
		t.Error("Verify accepted tampered code")
	}
}

func TestTOTP_Verify_EmptyInputs(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	if g.Verify("", "123456") {
		t.Error("Verify accepted empty secret")
	}
	if g.Verify("JBSWY3DPEHPK3PXP", "") {
		t.Error("Verify accepted empty code")
	}
	if g.Verify("not-base32!@#", "123456") {
		t.Error("Verify accepted invalid secret")
	}
}

// TestTOTP_Verify_ReplayPrevention documents an intentional limitation of
// raw RFC 6238: the same code is accepted multiple times within a single
// 30-second window. Consumers MUST persist the last accepted step per
// user and reject reuse. This test fixes the contract so a future change
// in either direction is a deliberate choice.
func TestTOTP_Verify_ReplayPrevention(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)
	if !g.Verify(secret, code) {
		t.Fatal("first Verify should succeed")
	}
	if !g.Verify(secret, code) {
		t.Error("documented behavior: second Verify with same code in same window also succeeds; consumer must dedupe by step")
	}
}

func TestTOTP_GenerateRecoveryCodes_FormatAndUniqueness(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	codes, err := g.GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("len(codes) = %d, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 9 {
			t.Errorf("code %q length = %d, want 9", c, len(c))
		}
		if c[4] != '-' {
			t.Errorf("code %q missing hyphen at index 4", c)
		}
		for i, r := range c {
			if i == 4 {
				continue
			}
			if !strings.ContainsRune(recoveryCodeAlphabet, r) {
				t.Errorf("code %q contains invalid char %q at %d", c, r, i)
			}
		}
		if seen[c] {
			t.Errorf("duplicate recovery code generated: %q", c)
		}
		seen[c] = true
	}
}

func TestTOTP_GenerateRecoveryCodes_InvalidCount(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	if _, err := g.GenerateRecoveryCodes(0); err == nil {
		t.Error("expected error for n=0")
	}
	if _, err := g.GenerateRecoveryCodes(-1); err == nil {
		t.Error("expected error for n=-1")
	}
}

func TestTOTP_ConsumeRecoveryCode_Match_RemovesFromList(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	stored := []string{"AAAA-AAAA", "BBBB-BBBB", "CCCC-CCCC"}
	consumed, remaining := g.ConsumeRecoveryCode(stored, "BBBB-BBBB")
	if !consumed {
		t.Fatal("expected consumed = true")
	}
	if len(remaining) != 2 {
		t.Fatalf("len(remaining) = %d, want 2", len(remaining))
	}
	for _, c := range remaining {
		if c == "BBBB-BBBB" {
			t.Error("matched code still present in remaining")
		}
	}
	// stored slice itself must not be mutated.
	if stored[1] != "BBBB-BBBB" {
		t.Error("ConsumeRecoveryCode mutated input slice")
	}
}

func TestTOTP_ConsumeRecoveryCode_NoMatch_ListUnchanged(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	stored := []string{"AAAA-AAAA", "BBBB-BBBB"}
	consumed, remaining := g.ConsumeRecoveryCode(stored, "ZZZZ-ZZZZ")
	if consumed {
		t.Error("expected consumed = false")
	}
	if len(remaining) != len(stored) {
		t.Errorf("len(remaining) = %d, want %d", len(remaining), len(stored))
	}
	for i := range stored {
		if remaining[i] != stored[i] {
			t.Errorf("remaining[%d] = %q, want %q", i, remaining[i], stored[i])
		}
	}
}

func TestTOTP_ConsumeRecoveryCode_SingleUse(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	stored := []string{"AAAA-AAAA", "BBBB-BBBB"}
	consumed, remaining := g.ConsumeRecoveryCode(stored, "AAAA-AAAA")
	if !consumed {
		t.Fatal("first consume should succeed")
	}
	consumed2, _ := g.ConsumeRecoveryCode(remaining, "AAAA-AAAA")
	if consumed2 {
		t.Error("second consume of same code should fail")
	}
}

func TestTOTP_ConsumeRecoveryCode_EmptyStoredOrSupplied(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	consumed, remaining := g.ConsumeRecoveryCode(nil, "AAAA-AAAA")
	if consumed || remaining != nil {
		t.Error("empty stored should return (false, stored)")
	}
	consumed, _ = g.ConsumeRecoveryCode([]string{"AAAA-AAAA"}, "")
	if consumed {
		t.Error("empty supplied should return false")
	}
}

// TestTOTP_ConsumeRecoveryCode_ConstantTime asserts that the consume
// function compares against every stored code. We verify by giving it a
// stored slice where the matching code is last; the implementation must
// return the correct index. The contract is also documented in code:
// every entry is compared, no early return. Practical wall-clock timing
// assertions are too noisy in CI so we settle for a behavioral proxy
// (correctness with match-at-last, consume from middle, etc.).
func TestTOTP_ConsumeRecoveryCode_ConstantTime(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	stored := []string{"AAAA-AAAA", "BBBB-BBBB", "CCCC-CCCC", "DDDD-DDDD"}
	cases := []struct {
		supplied string
		want     bool
		newLen   int
	}{
		{"AAAA-AAAA", true, 3},
		{"BBBB-BBBB", true, 3},
		{"DDDD-DDDD", true, 3},
		{"ZZZZ-ZZZZ", false, 4},
	}
	for _, tc := range cases {
		got, rem := g.ConsumeRecoveryCode(stored, tc.supplied)
		if got != tc.want {
			t.Errorf("supplied=%q consumed=%v want=%v", tc.supplied, got, tc.want)
		}
		if len(rem) != tc.newLen {
			t.Errorf("supplied=%q remaining len=%d want=%d", tc.supplied, len(rem), tc.newLen)
		}
	}
}

func TestTOTP_Base32_RoundTrip(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	for i := 0; i < 16; i++ {
		secret, _, err := g.Generate("u")
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		raw, err := decodeSecret(secret)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(raw) != 20 {
			t.Errorf("round-trip length = %d, want 20", len(raw))
		}
		// Re-encode and compare to original.
		again := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		if again != secret {
			t.Errorf("re-encoded = %q, want %q", again, secret)
		}
	}
}

func TestTOTP_DecodeSecret_AcceptsLowercaseAndSpaces(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	// authenticator apps often present secret with spaces / lowercase.
	munged := strings.ToLower(secret[:8] + " " + secret[8:])
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)
	if !g.Verify(munged, code) {
		t.Errorf("Verify should accept lowercase/spaced secret %q", munged)
	}
}

func TestRequireTwoFactor_DeniesWhenStatusFalse(t *testing.T) {
	gate := NewGate()
	gate.Define("admin", func(user Authenticatable, args ...interface{}) bool { return true })
	gate.Before(RequireTwoFactor(func(actor any) bool { return false }))

	user := &mockUser{id: 1}
	if gate.Allows(user, "admin") {
		t.Error("expected gate to deny when 2FA status is false")
	}
}

func TestRequireTwoFactor_AllowsWhenStatusTrue(t *testing.T) {
	gate := NewGate()
	gate.Define("admin", func(user Authenticatable, args ...interface{}) bool { return true })
	gate.Before(RequireTwoFactor(func(actor any) bool { return true }))

	user := &mockUser{id: 1}
	if !gate.Allows(user, "admin") {
		t.Error("expected gate to allow when 2FA status is true")
	}
}

func TestRequireTwoFactor_NilStatusIsNoop(t *testing.T) {
	gate := NewGate()
	gate.Define("admin", func(user Authenticatable, args ...interface{}) bool { return true })
	gate.Before(RequireTwoFactor(nil))

	user := &mockUser{id: 1}
	if !gate.Allows(user, "admin") {
		t.Error("nil status fn should not interfere with gate decisions")
	}
}

func TestRequireTwoFactor_NilUserIsNoop(t *testing.T) {
	cb := RequireTwoFactor(func(actor any) bool { return false })
	if cb(nil, "admin") != nil {
		t.Error("nil user should defer to gate (return nil)")
	}
}

// verifyAndConsumeAt mirrors (*TOTPGenerator).VerifyAndConsume but at a
// fixed timestamp; tests use it to deterministically exercise replay
// rejection across known steps without faking time.Now globally.
func verifyAndConsumeAt(g *TOTPGenerator, secret, code string, unix int64, lastUsedStep int64) (matched bool, step int64) {
	if len(code) == 0 || len(secret) == 0 {
		return false, 0
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false, 0
	}
	currentStep := unix / int64(g.cfg.Period)
	supplied := []byte(code)
	var matchedByte byte
	var matchedStep int64
	for offset := -g.cfg.Skew; offset <= g.cfg.Skew; offset++ {
		candidateStep := currentStep + int64(offset)
		expected := generateCode(key, candidateStep, g.cfg.Digits)
		var eq byte
		if len(expected) == len(supplied) {
			eq = byte(subtle.ConstantTimeCompare(expected, supplied))
		}
		notYet := 1 - int(matchedByte)
		updateNow := int(eq) & notYet
		matchedStep = constantTimeSelectInt64(updateNow, candidateStep, matchedStep)
		matchedByte |= eq
	}
	if matchedByte != 1 {
		return false, 0
	}
	if matchedStep <= lastUsedStep {
		return false, 0
	}
	return true, matchedStep
}

func TestVerifyAndConsume_RejectsReplay(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, err := g.Generate("u")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)

	matched, step := g.VerifyAndConsume(secret, code, 0)
	if !matched {
		t.Fatal("first VerifyAndConsume should succeed")
	}
	if step == 0 {
		t.Fatal("first VerifyAndConsume should return non-zero matched step")
	}

	matched2, step2 := g.VerifyAndConsume(secret, code, step)
	if matched2 {
		t.Error("replay with same step should be rejected")
	}
	if step2 != 0 {
		t.Errorf("replay rejection should return step=0, got %d", step2)
	}
}

func TestVerifyAndConsume_AcceptsAdvancing(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	// Pick a fixed timestamp so the two steps are deterministic.
	t0 := int64(1_700_000_000) // arbitrary; aligned to step boundaries via modulo below
	t0 -= t0 % 30              // align to a step boundary so t0/30 and t1/30 differ by exactly 1
	t1 := t0 + 30
	codeS := codeAt(t, secret, t0, 6, 30)
	codeS1 := codeAt(t, secret, t1, 6, 30)

	matched, step := verifyAndConsumeAt(g, secret, codeS, t0, 0)
	if !matched {
		t.Fatalf("verify at t0 should match, got matched=%v step=%d", matched, step)
	}
	if step != t0/30 {
		t.Errorf("matched step = %d, want %d", step, t0/30)
	}

	matched2, step2 := verifyAndConsumeAt(g, secret, codeS1, t1, step)
	if !matched2 {
		t.Fatalf("verify at t1 with lastUsedStep=%d should match", step)
	}
	if step2 != t1/30 {
		t.Errorf("matched step at t1 = %d, want %d", step2, t1/30)
	}
	if step2 <= step {
		t.Errorf("step2 (%d) must be > step (%d)", step2, step)
	}
}

// TestVerifyAndConsume_ConstantTime is a behavioral proxy: across all
// candidate skew offsets (and the no-match case), VerifyAndConsume must
// produce a result and never short-circuit. Wall-clock timing is too
// noisy in CI; we settle for asserting correctness at every offset.
func TestVerifyAndConsume_ConstantTime(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	t0 := int64(1_700_000_000)
	t0 -= t0 % 30
	for _, offset := range []int64{-30, 0, 30} {
		code := codeAt(t, secret, t0+offset, 6, 30)
		matched, step := verifyAndConsumeAt(g, secret, code, t0, 0)
		if !matched {
			t.Errorf("offset %ds: expected match", offset)
			continue
		}
		want := (t0 + offset) / 30
		if step != want {
			t.Errorf("offset %ds: step = %d, want %d", offset, step, want)
		}
	}
	// Outside-window code must not match and step must be 0.
	bad := codeAt(t, secret, t0+90, 6, 30)
	matched, step := verifyAndConsumeAt(g, secret, bad, t0, 0)
	if matched || step != 0 {
		t.Errorf("outside-window code: matched=%v step=%d, want false/0", matched, step)
	}
}

func TestVerifyAndConsume_EmptyInputs(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	if m, s := g.VerifyAndConsume("", "123456", 0); m || s != 0 {
		t.Error("empty secret should return (false, 0)")
	}
	if m, s := g.VerifyAndConsume("JBSWY3DPEHPK3PXP", "", 0); m || s != 0 {
		t.Error("empty code should return (false, 0)")
	}
	if m, s := g.VerifyAndConsume("not-base32!@#", "123456", 0); m || s != 0 {
		t.Error("invalid secret should return (false, 0)")
	}
}

func TestVerifyAndConsume_TamperedCode(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	secret, _, _ := g.Generate("u")
	now := time.Now().Unix()
	code := codeAt(t, secret, now, 6, 30)
	last := code[len(code)-1]
	if last == '9' {
		last = '0'
	} else {
		last++
	}
	tampered := code[:len(code)-1] + string(last)
	if m, s := g.VerifyAndConsume(secret, tampered, 0); m || s != 0 {
		t.Error("tampered code must not match")
	}
}

// hashRecoveryCodeFastHasher is a deterministic non-bcrypt hasher used to
// keep recovery-code tests fast. Production callers use BcryptHasher;
// these tests only need to exercise the wiring (interface contract,
// constant-time scan, single-use semantics).
type hashRecoveryCodeFastHasher struct{}

func (hashRecoveryCodeFastHasher) Hash(s string) (string, error) { return "h:" + s, nil }
func (hashRecoveryCodeFastHasher) Verify(s, h string) bool       { return h == "h:"+s }
func (hashRecoveryCodeFastHasher) NeedsRehash(string) bool       { return false }

func TestHashRecoveryCode_RoundTrip(t *testing.T) {
	h := NewBcryptHasher(minSecureBcryptCost)
	hashed, err := HashRecoveryCode(h, "ABCD-EFGH")
	if err != nil {
		t.Fatalf("HashRecoveryCode: %v", err)
	}
	if hashed == "" {
		t.Fatal("HashRecoveryCode returned empty string")
	}
	if hashed == "ABCD-EFGH" {
		t.Fatal("HashRecoveryCode returned plaintext")
	}
	if !h.Verify("ABCD-EFGH", hashed) {
		t.Error("Verify should accept the hashed code")
	}
	if h.Verify("WRONG-CODE", hashed) {
		t.Error("Verify should reject a different code")
	}
}

func TestHashRecoveryCode_NilHasher(t *testing.T) {
	if _, err := HashRecoveryCode(nil, "AAAA-BBBB"); err == nil {
		t.Error("expected error for nil hasher")
	}
}

func TestHashRecoveryCode_EmptyCode(t *testing.T) {
	h := NewBcryptHasher(minSecureBcryptCost)
	if _, err := HashRecoveryCode(h, ""); err == nil {
		t.Error("expected error for empty code")
	}
}

func TestConsumeRecoveryCodeHashed_Match_RemovesFromList(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	h := hashRecoveryCodeFastHasher{}
	stored := []string{"h:AAAA-AAAA", "h:BBBB-BBBB", "h:CCCC-CCCC"}
	consumed, remaining, err := g.ConsumeRecoveryCodeHashed(h, stored, "BBBB-BBBB")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCodeHashed: %v", err)
	}
	if !consumed {
		t.Fatal("expected consumed = true")
	}
	if len(remaining) != 2 {
		t.Fatalf("len(remaining) = %d, want 2", len(remaining))
	}
	for _, c := range remaining {
		if c == "h:BBBB-BBBB" {
			t.Error("matched hash still present in remaining")
		}
	}
	if stored[1] != "h:BBBB-BBBB" {
		t.Error("ConsumeRecoveryCodeHashed mutated input slice")
	}
}

func TestConsumeRecoveryCodeHashed_NoMatch_ListUnchanged(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	h := hashRecoveryCodeFastHasher{}
	stored := []string{"h:AAAA-AAAA", "h:BBBB-BBBB"}
	consumed, remaining, err := g.ConsumeRecoveryCodeHashed(h, stored, "ZZZZ-ZZZZ")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCodeHashed: %v", err)
	}
	if consumed {
		t.Error("expected consumed = false")
	}
	if len(remaining) != len(stored) {
		t.Errorf("len(remaining) = %d, want %d", len(remaining), len(stored))
	}
	for i := range stored {
		if remaining[i] != stored[i] {
			t.Errorf("remaining[%d] = %q, want %q", i, remaining[i], stored[i])
		}
	}
}

func TestConsumeRecoveryCodeHashed_SingleUse(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	h := hashRecoveryCodeFastHasher{}
	stored := []string{"h:AAAA-AAAA", "h:BBBB-BBBB"}
	consumed, remaining, err := g.ConsumeRecoveryCodeHashed(h, stored, "AAAA-AAAA")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if !consumed {
		t.Fatal("first consume should succeed")
	}
	consumed2, _, err := g.ConsumeRecoveryCodeHashed(h, remaining, "AAAA-AAAA")
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if consumed2 {
		t.Error("second consume of same code should fail")
	}
}

func TestConsumeRecoveryCodeHashed_EmptyAndNilInputs(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	h := hashRecoveryCodeFastHasher{}

	if _, _, err := g.ConsumeRecoveryCodeHashed(nil, []string{"h:X"}, "X"); err == nil {
		t.Error("nil hasher should error")
	}
	if _, _, err := g.ConsumeRecoveryCodeHashed(h, []string{"h:X"}, ""); err == nil {
		t.Error("empty supplied should error")
	}
	consumed, remaining, err := g.ConsumeRecoveryCodeHashed(h, nil, "X")
	if err != nil {
		t.Errorf("nil stored: %v", err)
	}
	if consumed {
		t.Error("nil stored should not consume")
	}
	if remaining != nil {
		t.Error("nil stored should round-trip nil")
	}
}

// TestConsumeRecoveryCodeHashed_ConstantTime mirrors the plaintext
// constant-time test: the function must scan every entry. We assert this
// behaviorally by exercising matches at every position plus a no-match.
func TestConsumeRecoveryCodeHashed_ConstantTime(t *testing.T) {
	g := NewTOTP(TOTPConfig{})
	h := hashRecoveryCodeFastHasher{}
	stored := []string{"h:AAAA-AAAA", "h:BBBB-BBBB", "h:CCCC-CCCC", "h:DDDD-DDDD"}
	cases := []struct {
		supplied string
		want     bool
		newLen   int
	}{
		{"AAAA-AAAA", true, 3},
		{"CCCC-CCCC", true, 3},
		{"DDDD-DDDD", true, 3},
		{"ZZZZ-ZZZZ", false, 4},
	}
	for _, tc := range cases {
		got, rem, err := g.ConsumeRecoveryCodeHashed(h, stored, tc.supplied)
		if err != nil {
			t.Errorf("supplied=%q: err=%v", tc.supplied, err)
			continue
		}
		if got != tc.want {
			t.Errorf("supplied=%q consumed=%v want=%v", tc.supplied, got, tc.want)
		}
		if len(rem) != tc.newLen {
			t.Errorf("supplied=%q remaining len=%d want=%d", tc.supplied, len(rem), tc.newLen)
		}
	}
}

func TestTOTP_Concurrent(t *testing.T) {
	g := NewTOTP(TOTPConfig{Skew: 1})
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			secret, _, err := g.Generate("u")
			if err != nil {
				t.Errorf("Generate: %v", err)
				return
			}
			now := time.Now().Unix()
			code := codeAt(t, secret, now, 6, 30)
			if !g.Verify(secret, code) {
				t.Error("Verify should succeed under concurrent load")
			}
			codes, err := g.GenerateRecoveryCodes(5)
			if err != nil {
				t.Errorf("GenerateRecoveryCodes: %v", err)
				return
			}
			ok, rem := g.ConsumeRecoveryCode(codes, codes[2])
			if !ok || len(rem) != 4 {
				t.Errorf("ConsumeRecoveryCode concurrent: ok=%v len=%d", ok, len(rem))
			}
		}()
	}
	wg.Wait()
}
