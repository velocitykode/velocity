package auth

import (
	"crypto/rsa"
	"errors"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// E-02: JWT signing-key rotation. PreviousSecrets / PreviousRSAPublicKeys let
// operators rotate the active minting key while still verifying tokens signed
// under the prior key, so a quarterly rotation does not force-logout every
// outstanding access AND refresh token at once. These tests pin the
// accept-on-verify, mint-current-only semantics.

const (
	rotationSecretA = "rotation-secret-A-aaaaaaaaaaaaaaaaaaaaaa"
	rotationSecretB = "rotation-secret-B-bbbbbbbbbbbbbbbbbbbbbb"
	rotationSecretC = "rotation-secret-C-cccccccccccccccccccccc"
	rotationSecretD = "rotation-secret-D-dddddddddddddddddddddd"
)

func TestValidate_AcceptsCurrentSecret(t *testing.T) {
	mgr, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretA,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	tok, err := mgr.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := mgr.ValidateToken(tok); err != nil {
		t.Fatalf("ValidateToken: unexpected error: %v", err)
	}
}

// TestValidate_AcceptsPreviousSecret: mint under A, rotate active to B with
// PreviousSecrets=[A], verify A-token. This is the core E-02 fix: outstanding
// tokens survive a key rotation until they expire naturally.
func TestValidate_AcceptsPreviousSecret(t *testing.T) {
	minter, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretA,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager minter: %v", err)
	}
	tok, err := minter.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	rotated, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretB,
		PreviousSecrets: []string{rotationSecretA},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	if _, err := rotated.ValidateToken(tok); err != nil {
		t.Fatalf("rotated.ValidateToken under retired secret: %v", err)
	}
}

// TestValidate_RejectsRetiredSecret: once a secret is dropped from
// PreviousSecrets entirely, tokens signed under it must fail.
func TestValidate_RejectsRetiredSecret(t *testing.T) {
	minter, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretA,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager minter: %v", err)
	}
	tok, err := minter.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	rotated, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretB,
		Algorithm: "HS256",
		TTL:       60,
		// PreviousSecrets intentionally empty: A is fully retired.
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	if _, err := rotated.ValidateToken(tok); err == nil {
		t.Fatal("expected validation error for fully-retired secret, got nil")
	}
}

// TestMint_UsesCurrentSecretOnly: PreviousSecrets must not influence minting.
// Tokens minted by the rotated manager are signed under B. A manager whose
// active secret is A (with B in PreviousSecrets) verifies them via the
// previous-key path, confirming we minted under current B (not under A).
func TestMint_UsesCurrentSecretOnly(t *testing.T) {
	rotated, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretB,
		PreviousSecrets: []string{rotationSecretA},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	tok, err := rotated.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// A manager that only knows A as the active secret must reject:
	// proves the token was NOT minted under A.
	onlyA, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretA,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager onlyA: %v", err)
	}
	if _, err := onlyA.ValidateToken(tok); err == nil {
		t.Fatal("expected onlyA.ValidateToken to reject B-signed token, got nil")
	}

	// And a manager whose active is A with PreviousSecrets=[B] verifies
	// the token under the previous-key path: confirms current-only minting.
	aWithBPrev, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretA,
		PreviousSecrets: []string{rotationSecretB},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager aWithBPrev: %v", err)
	}
	if _, err := aWithBPrev.ValidateToken(tok); err != nil {
		t.Fatalf("aWithBPrev.ValidateToken: %v", err)
	}
}

// TestValidate_OldestPreviousNotMatched: a token signed under an unrelated
// secret D is rejected even when PreviousSecrets=[A, B, C].
func TestValidate_OldestPreviousNotMatched(t *testing.T) {
	stranger, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretD,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager stranger: %v", err)
	}
	tok, err := stranger.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	mgr, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretA,
		PreviousSecrets: []string{rotationSecretB, rotationSecretC},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager mgr: %v", err)
	}
	if _, err := mgr.ValidateToken(tok); err == nil {
		t.Fatal("expected validation error for D-signed token, got nil")
	}
}

// TestValidate_MultiplePreviousSecrets_AnyMatches confirms the parser iterates
// the full set, not just the first entry.
func TestValidate_MultiplePreviousSecrets_AnyMatches(t *testing.T) {
	// Mint under C.
	minter, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretC,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager minter: %v", err)
	}
	tok, err := minter.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// Active A, previous [B, C]. C is the last entry, must still match.
	mgr, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretA,
		PreviousSecrets: []string{rotationSecretB, rotationSecretC},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager mgr: %v", err)
	}
	if _, err := mgr.ValidateToken(tok); err != nil {
		t.Fatalf("ValidateToken under C (last entry): %v", err)
	}
}

// --- RSA variants ---

func TestValidate_RSA_AcceptsCurrentKey(t *testing.T) {
	keyA := rsaKeyPair(t, 2048)
	mgr, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyA,
		RSAPublicKey:  &keyA.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	tok, err := mgr.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := mgr.ValidateToken(tok); err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
}

// TestValidate_RSA_AcceptsPreviousPublicKey: mint under keyA, rotate active to
// keyB with PreviousRSAPublicKeys=[&keyA.Public]. A-signed token must verify.
func TestValidate_RSA_AcceptsPreviousPublicKey(t *testing.T) {
	keyA := rsaKeyPair(t, 2048)
	keyB := rsaKeyPair(t, 2048)

	minter, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyA,
		RSAPublicKey:  &keyA.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager minter: %v", err)
	}
	tok, err := minter.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	rotated, err := NewJWTManager(JWTConfig{
		Algorithm:             "RS256",
		TTL:                   60,
		RSAPrivateKey:         keyB,
		RSAPublicKey:          &keyB.PublicKey,
		PreviousRSAPublicKeys: []interface{}{&keyA.PublicKey},
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	if _, err := rotated.ValidateToken(tok); err != nil {
		t.Fatalf("rotated.ValidateToken under retired public key: %v", err)
	}
}

func TestValidate_RSA_RejectsRetiredPublicKey(t *testing.T) {
	keyA := rsaKeyPair(t, 2048)
	keyB := rsaKeyPair(t, 2048)

	minter, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyA,
		RSAPublicKey:  &keyA.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager minter: %v", err)
	}
	tok, err := minter.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	rotated, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyB,
		RSAPublicKey:  &keyB.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	if _, err := rotated.ValidateToken(tok); err == nil {
		t.Fatal("expected validation error for fully-retired RSA key, got nil")
	}
}

func TestMint_RSA_UsesCurrentKeyOnly(t *testing.T) {
	keyA := rsaKeyPair(t, 2048)
	keyB := rsaKeyPair(t, 2048)

	rotated, err := NewJWTManager(JWTConfig{
		Algorithm:             "RS256",
		TTL:                   60,
		RSAPrivateKey:         keyB,
		RSAPublicKey:          &keyB.PublicKey,
		PreviousRSAPublicKeys: []interface{}{&keyA.PublicKey},
	})
	if err != nil {
		t.Fatalf("NewJWTManager rotated: %v", err)
	}
	tok, err := rotated.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// A manager that only knows A must reject: proves we minted under B.
	onlyA, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyA,
		RSAPublicKey:  &keyA.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager onlyA: %v", err)
	}
	if _, err := onlyA.ValidateToken(tok); err == nil {
		t.Fatal("expected onlyA.ValidateToken to reject B-signed RSA token, got nil")
	}
}

func TestValidate_RSA_OldestPreviousNotMatched(t *testing.T) {
	keyA := rsaKeyPair(t, 2048)
	keyB := rsaKeyPair(t, 2048)
	keyC := rsaKeyPair(t, 2048)
	keyD := rsaKeyPair(t, 2048)

	stranger, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: keyD,
		RSAPublicKey:  &keyD.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager stranger: %v", err)
	}
	tok, err := stranger.GenerateToken(&AuthUser{ID: 1})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	mgr, err := NewJWTManager(JWTConfig{
		Algorithm:             "RS256",
		TTL:                   60,
		RSAPrivateKey:         keyA,
		RSAPublicKey:          &keyA.PublicKey,
		PreviousRSAPublicKeys: []interface{}{&keyB.PublicKey, &keyC.PublicKey},
	})
	if err != nil {
		t.Fatalf("NewJWTManager mgr: %v", err)
	}
	if _, err := mgr.ValidateToken(tok); err == nil {
		t.Fatal("expected validation error for D-signed RSA token, got nil")
	}
}

// --- Config validation tests ---

func TestValidate_RejectsEmptyPreviousSecret(t *testing.T) {
	cfg := JWTConfig{
		Algorithm:       "HS256",
		Secret:          rotationSecretA,
		TTL:             60,
		PreviousSecrets: []string{""},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty previous secret")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsShortPreviousSecret(t *testing.T) {
	cfg := JWTConfig{
		Algorithm:       "HS256",
		Secret:          rotationSecretA,
		TTL:             60,
		PreviousSecrets: []string{"too-short"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for short previous secret")
	}
	if !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsDuplicatePreviousSecret(t *testing.T) {
	cfg := JWTConfig{
		Algorithm:       "HS256",
		Secret:          rotationSecretA,
		TTL:             60,
		PreviousSecrets: []string{rotationSecretA},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for previous secret equal to active")
	}
	if !strings.Contains(err.Error(), "duplicates active secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsRSAPreviousOnHMAC(t *testing.T) {
	key := rsaKeyPair(t, 2048)
	cfg := JWTConfig{
		Algorithm:             "HS256",
		Secret:                rotationSecretA,
		TTL:                   60,
		PreviousRSAPublicKeys: []interface{}{&key.PublicKey},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for RSA previous keys on HMAC config")
	}
	if !strings.Contains(err.Error(), "only valid for rsa algorithms") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsHMACPreviousOnRSA(t *testing.T) {
	key := rsaKeyPair(t, 2048)
	cfg := JWTConfig{
		Algorithm:       "RS256",
		TTL:             60,
		RSAPrivateKey:   key,
		RSAPublicKey:    &key.PublicKey,
		PreviousSecrets: []string{rotationSecretA},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for HMAC previous secrets on RSA config")
	}
	if !strings.Contains(err.Error(), "only valid for hmac algorithms") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_RejectsNilPreviousRSAKey(t *testing.T) {
	key := rsaKeyPair(t, 2048)
	cfg := JWTConfig{
		Algorithm:             "RS256",
		TTL:                   60,
		RSAPrivateKey:         key,
		RSAPublicKey:          &key.PublicKey,
		PreviousRSAPublicKeys: []interface{}{nil},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for nil previous RSA key")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestVerificationKeySet_Shape: white-box assertion that verificationKey()
// returns a single key when no previous keys are configured, and a
// VerificationKeySet otherwise. Guards against an accidental refactor that
// might wrap every call in a KeySet (which the jwt parser tolerates but
// makes the hot path slightly costlier and obscures intent).
func TestVerificationKeySet_Shape(t *testing.T) {
	// HMAC, no rotation: single []byte.
	hmacSingle, err := NewJWTManager(JWTConfig{
		Secret:    rotationSecretA,
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager hmacSingle: %v", err)
	}
	if _, ok := hmacSingle.verificationKey().([]byte); !ok {
		t.Fatalf("expected []byte for HMAC without rotation, got %T", hmacSingle.verificationKey())
	}

	// HMAC, with rotation: VerificationKeySet.
	hmacRotated, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretA,
		PreviousSecrets: []string{rotationSecretB},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager hmacRotated: %v", err)
	}
	ks, ok := hmacRotated.verificationKey().(jwt.VerificationKeySet)
	if !ok {
		t.Fatalf("expected VerificationKeySet for HMAC rotation, got %T", hmacRotated.verificationKey())
	}
	if got, want := len(ks.Keys), 2; got != want {
		t.Fatalf("VerificationKeySet len: got %d want %d", got, want)
	}

	// RSA, no rotation: single *rsa.PublicKey.
	key := rsaKeyPair(t, 2048)
	rsaSingle, err := NewJWTManager(JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: key,
		RSAPublicKey:  &key.PublicKey,
	})
	if err != nil {
		t.Fatalf("NewJWTManager rsaSingle: %v", err)
	}
	if _, ok := rsaSingle.verificationKey().(*rsa.PublicKey); !ok {
		t.Fatalf("expected *rsa.PublicKey for RSA without rotation, got %T", rsaSingle.verificationKey())
	}

	// RSA, with rotation: VerificationKeySet of length 2.
	prev := rsaKeyPair(t, 2048)
	rsaRotated, err := NewJWTManager(JWTConfig{
		Algorithm:             "RS256",
		TTL:                   60,
		RSAPrivateKey:         key,
		RSAPublicKey:          &key.PublicKey,
		PreviousRSAPublicKeys: []interface{}{&prev.PublicKey},
	})
	if err != nil {
		t.Fatalf("NewJWTManager rsaRotated: %v", err)
	}
	ks, ok = rsaRotated.verificationKey().(jwt.VerificationKeySet)
	if !ok {
		t.Fatalf("expected VerificationKeySet for RSA rotation, got %T", rsaRotated.verificationKey())
	}
	if got, want := len(ks.Keys), 2; got != want {
		t.Fatalf("VerificationKeySet len: got %d want %d", got, want)
	}
}

// TestValidate_RotationStillRejectsForgedAlgNone confirms the alg=none guard
// is unchanged by the rotation work. Forged none-alg token must be rejected
// even when verification has multiple keys to try.
func TestValidate_RotationStillRejectsForgedAlgNone(t *testing.T) {
	mgr, err := NewJWTManager(JWTConfig{
		Secret:          rotationSecretA,
		PreviousSecrets: []string{rotationSecretB},
		Algorithm:       "HS256",
		TTL:             60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	tok := forgeUnsignedToken(t, "none", map[string]interface{}{"uid": 1, "sub": "1"})
	if _, err := mgr.ValidateToken(tok); err == nil {
		t.Fatal("expected rejection for alg=none under rotation config")
	} else if !strings.Contains(err.Error(), "none") && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		// Library wording varies; just ensure we don't silently accept.
		t.Logf("rejection error (informational): %v", err)
	}
}
