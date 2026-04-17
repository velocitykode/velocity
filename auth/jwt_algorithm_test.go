package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// rsaKeyPair generates a single RSA key pair for tests.
func rsaKeyPair(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return k
}

// forgeUnsignedToken crafts a JWT with the given alg header and no signature.
// Used to confirm the validator rejects the "none" algorithm unconditionally.
func forgeUnsignedToken(t *testing.T, alg string, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]string{"alg": alg, "typ": "JWT"}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(h) + "." +
		base64.RawURLEncoding.EncodeToString(p) + "."
}

func TestNewJWTManager_ValidationRejects(t *testing.T) {
	tests := []struct {
		name    string
		cfg     JWTConfig
		wantErr string
	}{
		{
			name:    "empty secret for HMAC",
			cfg:     JWTConfig{Algorithm: "HS256", TTL: 60},
			wantErr: "secret must not be empty",
		},
		{
			name:    "short secret for HMAC",
			cfg:     JWTConfig{Algorithm: "HS256", Secret: "short", TTL: 60},
			wantErr: "at least 32 bytes",
		},
		{
			name:    "unsupported algorithm",
			cfg:     JWTConfig{Algorithm: "none", Secret: "abcdefghijklmnopqrstuvwxyz012345", TTL: 60},
			wantErr: "unsupported jwt algorithm",
		},
		{
			name:    "negative ttl",
			cfg:     JWTConfig{Algorithm: "HS256", Secret: "abcdefghijklmnopqrstuvwxyz012345", TTL: -1},
			wantErr: "ttl must be positive",
		},
		{
			name:    "rs256 missing keys",
			cfg:     JWTConfig{Algorithm: "RS256", TTL: 60},
			wantErr: "rsa key pair is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewJWTManager(tt.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "velocity/auth:") {
				t.Errorf("error %q missing velocity/auth prefix", err.Error())
			}
		})
	}
}

func TestJWTValidateToken_AlgorithmMatrix(t *testing.T) {
	const hmacSecret = "super-secret-key-for-tests-at-least-32"
	hmacConfig := JWTConfig{Secret: hmacSecret, Algorithm: "HS256", TTL: 60}
	hmacMgr, err := NewJWTManager(hmacConfig)
	if err != nil {
		t.Fatalf("NewJWTManager hmac: %v", err)
	}

	rsaKey := rsaKeyPair(t, 2048)
	rsaConfig := JWTConfig{
		Algorithm:     "RS256",
		TTL:           60,
		RSAPrivateKey: rsaKey,
		RSAPublicKey:  &rsaKey.PublicKey,
	}
	rsaMgr, err := NewJWTManager(rsaConfig)
	if err != nil {
		t.Fatalf("NewJWTManager rsa: %v", err)
	}

	user := &AuthUser{ID: 42}

	// Table-driven matrix for configured algo vs token algo.
	type expectation struct {
		tokenAlg string
		wantErr  bool
	}
	matrix := []struct {
		name        string
		manager     *JWTManager
		cases       []expectation
		otherAlgKey jwt.SigningMethod
	}{
		{
			name:    "hmac-configured manager",
			manager: hmacMgr,
			cases: []expectation{
				{"HS256", false},
				{"HS384", true}, // Configured alg is HS256 specifically
				{"HS512", true},
				{"RS256", true},
				{"RS384", true},
				{"RS512", true},
				{"none", true},
			},
		},
		{
			name:    "rsa-configured manager",
			manager: rsaMgr,
			cases: []expectation{
				{"RS256", false},
				{"RS384", true},
				{"RS512", true},
				{"HS256", true},
				{"HS384", true},
				{"HS512", true},
				{"none", true},
			},
		},
	}

	for _, tc := range matrix {
		for _, c := range tc.cases {
			t.Run(tc.name+"/"+c.tokenAlg, func(t *testing.T) {
				var tokenStr string
				switch c.tokenAlg {
				case "none":
					tokenStr = forgeUnsignedToken(t, "none", map[string]interface{}{
						"uid": 42,
						"sub": "42",
					})
				case "HS256", "HS384", "HS512":
					method := jwt.SigningMethodHS256
					if c.tokenAlg == "HS384" {
						method = jwt.SigningMethodHS384
					}
					if c.tokenAlg == "HS512" {
						method = jwt.SigningMethodHS512
					}
					tok := jwt.NewWithClaims(method, Claims{
						UserID:    42,
						TokenType: "access",
					})
					signed, err := tok.SignedString([]byte(hmacSecret))
					if err != nil {
						t.Fatalf("sign: %v", err)
					}
					tokenStr = signed
				case "RS256", "RS384", "RS512":
					method := jwt.SigningMethodRS256
					if c.tokenAlg == "RS384" {
						method = jwt.SigningMethodRS384
					}
					if c.tokenAlg == "RS512" {
						method = jwt.SigningMethodRS512
					}
					tok := jwt.NewWithClaims(method, Claims{
						UserID:    42,
						TokenType: "access",
					})
					signed, err := tok.SignedString(rsaKey)
					if err != nil {
						t.Fatalf("sign rsa: %v", err)
					}
					tokenStr = signed
				}

				_, err := tc.manager.ValidateToken(tokenStr)
				if c.wantErr && err == nil {
					t.Fatalf("expected validation error for alg=%q, got nil", c.tokenAlg)
				}
				if !c.wantErr && err != nil {
					t.Fatalf("unexpected error for alg=%q: %v", c.tokenAlg, err)
				}
			})
		}
	}

	// Happy-path: valid HS384 with HS384 manager.
	hmac384Mgr, err := NewJWTManager(JWTConfig{Secret: hmacSecret, Algorithm: "HS384", TTL: 60})
	if err != nil {
		t.Fatalf("NewJWTManager hs384: %v", err)
	}
	tok, err := hmac384Mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken hs384: %v", err)
	}
	if _, err := hmac384Mgr.ValidateToken(tok); err != nil {
		t.Fatalf("ValidateToken hs384: %v", err)
	}
}

// TestJWTValidateToken_NoneRejectedUnconditionally ensures that "none" and
// blank algorithms never reach the signing-key callback.
func TestJWTValidateToken_NoneRejectedUnconditionally(t *testing.T) {
	mgr, err := NewJWTManager(JWTConfig{
		Secret:    "super-secret-key-for-tests-at-least-32",
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	// Classic "alg=none" attack.
	tok := forgeUnsignedToken(t, "none", map[string]interface{}{"uid": 1})
	if _, err := mgr.ValidateToken(tok); err == nil {
		t.Fatal("expected rejection for alg=none")
	}

	// Fake blank algorithm header.
	tok = forgeUnsignedToken(t, "", map[string]interface{}{"uid": 1})
	if _, err := mgr.ValidateToken(tok); err == nil {
		t.Fatal("expected rejection for alg=\"\"")
	}
}

// TestJWTConfig_Validate_Allowlist ensures Validate() gates algorithm and TTL
// checks.
func TestJWTConfig_Validate_Allowlist(t *testing.T) {
	cases := map[string]bool{
		"HS256": true,
		"HS384": true,
		"HS512": true,
		"RS256": true,
		"RS384": true,
		"RS512": true,
		"none":  false,
		"":      true, // "" is normalized to HS256 inside Validate
		"EEE":   false,
	}
	rsaKey := rsaKeyPair(t, 2048)
	for alg, ok := range cases {
		t.Run("alg-"+alg, func(t *testing.T) {
			cfg := JWTConfig{Algorithm: alg, TTL: 60, Secret: "super-secret-key-for-tests-at-least-32"}
			if strings.HasPrefix(alg, "RS") {
				cfg.RSAPrivateKey = rsaKey
				cfg.RSAPublicKey = &rsaKey.PublicKey
			}
			err := cfg.Validate()
			if ok && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if !ok && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// failingRandReader is an io.Reader that always returns err.
type failingRandReader struct{ err error }

func (f failingRandReader) Read(p []byte) (int, error) { return 0, f.err }

// TestGenerateJTI_RandFailure ensures we surface rand.Read failures as errors
// rather than panicking. We achieve this by temporarily swapping
// crypto/rand.Reader for a failing reader via a small indirection.
//
// NOTE: generateJTI calls rand.Read which uses the package-level Reader,
// so this test swaps it out via the exported package variable.
func TestGenerateJTI_RandFailure(t *testing.T) {
	// Verify the happy path first.
	got, err := generateJTI()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty jti")
	}

	// Swap rand.Reader to a failing reader to force the error path.
	orig := randReader
	defer func() { randReader = orig }()
	randReader = failingRandReader{err: errors.New("boom")}
	if _, err := generateJTIWithReader(randReader); err == nil {
		t.Fatal("expected error from generateJTIWithReader")
	} else if !strings.HasPrefix(err.Error(), "velocity/auth:") {
		t.Fatalf("error missing prefix: %v", err)
	}
}
