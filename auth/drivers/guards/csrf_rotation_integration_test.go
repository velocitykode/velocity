package guards

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	csrfstores "github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// decryptSessionID extracts the plaintext session ID from a vel_session
// cookie produced by CookieStore. The cookie payload is AES-256-GCM
// encrypted JSON with shape {"id":"...","data":...,"flash":...}; the
// integration test reads .id to verify CSRF store keying against the
// rotated plaintext id.
func decryptSessionID(t *testing.T, enc crypto.Encryptor, cookieValue string) string {
	t.Helper()
	plaintext, err := enc.Decrypt(cookieValue)
	if err != nil {
		t.Fatalf("decrypt cookie: %v", err)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		t.Fatalf("unmarshal session payload: %v", err)
	}
	if payload.ID == "" {
		t.Fatal("session payload had empty id")
	}
	return payload.ID
}

// TestCSRFRotation_RememberCookieRevival_RotatesAndPersists is the
// end-to-end integration test that closes the loop on H-02 across the
// post-G2 baseline. It proves the rotation actually reaches the client
// via the H-05 + F3 save-at-end pre-commit hook, and that a token
// minted under a planted pre-revival session id is dropped from the
// CSRF store while the post-revival id gets a fresh token.
//
// Scenario (session-fixation defense + CSRF orphan cleanup):
//  1. Attacker plants session cookie P (the fresh-but-empty session id
//     "planted-id" minted via store.Create + store.Save). A CSRF token
//     T_planted is seeded under "planted-id" in the CSRF store - this
//     models the case where the attacker drove a CSRF refresh under
//     their planted id before luring the victim.
//  2. Victim logs in once with remember=true (separately, so the test
//     also has a valid remember cookie). The login cookie is discarded.
//  3. A new request arrives carrying BOTH the planted session cookie P
//     AND the victim's remember cookie. It hits /whoami which calls
//     scheme.User; the recall path runs because the planted session has
//     no user_id.
//  4. anchorRecalledUser captures oldID=planted-id, Regenerates to
//     newID, RotateToken(planted-id, newID), then Put("user_id", u1).
//     SessionMiddleware's pre-commit hook flushes vel_session=cookieB.
//
// Assertions:
//   - response carries Set-Cookie: vel_session=... distinct from P,
//     decrypting to a different plaintext id (post-revival);
//   - CSRF store has a token under the post-revival id;
//   - CSRF store has NO token under "planted-id" (RotateToken cleared
//     the orphan);
//   - follow-up request bearing the new cookie reads user_id back via
//     scheme.User, proving the rotation persisted across requests.
//
// Pre-G2 this test could not exist: no save-at-end primitive flushed the
// rotated cookie. With the pre-commit hook + the CSRF rotator wired
// into anchorRecalledUser, this test pins the full lifecycle.
func TestCSRFRotation_RememberCookieRevival_RotatesAndPersists(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	sessCfg := auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	store, err := session.NewCookieStore(sessCfg, enc)
	if err != nil {
		t.Fatalf("NewCookieStore: %v", err)
	}
	// Use the existing rememberRevivalStore helper (defined in
	// session_recaller_test.go) so the user's stored remember-token
	// hash matches the cookie minted by Login + setRememberCookie.
	userStore := &rememberRevivalStore{user: &revokeTestUser{id: "u1"}}
	scheme, err := NewSessionScheme(userStore, sessCfg, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	// Real CSRF instance backed by an in-memory store, with a resolver
	// that pulls the plaintext id off the encrypted vel_session cookie
	// (mirrors app.go's auto-installed resolver).
	csrfCfg := csrf.DefaultConfig()
	csrfCfg.Secure = false
	csrfCfg.Store = csrfstores.NewSessionStore()
	csrfCfg.SessionIDResolver = func(r *http.Request) (string, error) {
		c, err := r.Cookie("vel_session")
		if err != nil || c.Value == "" {
			return "", csrf.ErrNoSession
		}
		plaintext, err := enc.Decrypt(c.Value)
		if err != nil {
			return "", csrf.ErrNoSession
		}
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(plaintext), &payload); err != nil || payload.ID == "" {
			return "", csrf.ErrNoSession
		}
		return payload.ID, nil
	}
	csrfInstance, err := csrf.NewE(csrfCfg)
	if err != nil {
		t.Fatalf("csrf.NewE: %v", err)
	}
	scheme.SetCSRFTokenRotator(csrfInstance)

	// Step 1: mint a planted session cookie via the real CookieStore.
	// The session is a fresh empty bag (no user_id); the cookie is
	// what an attacker would inject into the victim's jar via a
	// subdomain cookie-tossing primitive or a previously captured pre-
	// auth cookie. Seed a CSRF token under the planted id so the
	// orphan-deletion assertion has something to delete.
	plantedSession, err := store.Create("")
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	plantedID := plantedSession.ID()
	if plantedID == "" {
		t.Fatal("store.Create returned session with empty id")
	}
	// Force modified=true so Save actually writes a cookie. Putting
	// any value flips the BaseSession.modified flag.
	plantedSession.Put("fixation-marker", "yes")
	plantedRec := httptest.NewRecorder()
	if err := store.Save(plantedRec, plantedSession); err != nil {
		t.Fatalf("store.Save (planted): %v", err)
	}
	var plantedCookie *http.Cookie
	for _, c := range plantedRec.Result().Cookies() {
		if c.Name == "vel_session" {
			plantedCookie = c
		}
	}
	if plantedCookie == nil {
		t.Fatal("planted store.Save emitted no vel_session cookie")
	}
	if err := csrfCfg.Store.Set(plantedID, "T_planted_orphan_token_value"); err != nil {
		t.Fatalf("seed planted CSRF token: %v", err)
	}

	// Step 2: a separate Login produces the victim's remember cookie.
	// The session cookie from this Login is discarded; only the
	// remember cookie travels to step 3.
	loginReq := httptest.NewRequest(http.MethodPost, "/login", nil)
	loginReq = WithSessionContext(loginReq)
	loginW := httptest.NewRecorder()
	if err := scheme.Login(loginW, loginReq, &revokeTestUser{id: "u1"}, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	var rememberCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == "remember_vel_session" {
			rememberCookie = c
		}
	}
	if rememberCookie == nil {
		t.Fatal("login emitted no remember_vel_session cookie")
	}

	// Step 3: build a router with save-at-end middleware + handler
	// that drives the recall via scheme.User.
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Get("/whoami", func(c *router.Context) error {
		u := scheme.User(c.Request)
		if u == nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"err": "anon"})
		}
		id, _ := u.GetAuthIdentifier().(string)
		return c.JSON(http.StatusOK, map[string]string{"user_id": id})
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/whoami", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Carry BOTH the planted session cookie AND the remember cookie.
	req.AddCookie(plantedCookie)
	req.AddCookie(rememberCookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/whoami status = %d; want 200", resp.StatusCode)
	}

	// Assertion 1: response carries a NEW vel_session cookie distinct
	// from the planted one. Pre-commit hook flushed the rotation
	// ahead of c.JSON's header commit.
	var rotatedCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "vel_session" {
			rotatedCookie = c
		}
	}
	if rotatedCookie == nil {
		t.Fatal("revival response carries no Set-Cookie: vel_session; rotation did not reach the client")
	}
	if rotatedCookie.Value == plantedCookie.Value {
		t.Fatal("revival response reused the planted vel_session value; rotation did not actually rotate")
	}
	rotatedID := decryptSessionID(t, enc, rotatedCookie.Value)
	if rotatedID == plantedID {
		t.Fatalf("rotated plaintext id %q == planted id; Session.Regenerate did not run inside anchorRecalledUser", plantedID)
	}

	// Assertion 2: CSRF store has a token under the rotated id.
	// rotator.RotateToken(plantedID, rotatedID) seeded the new entry.
	if _, err := csrfCfg.Store.Get(rotatedID); err != nil {
		t.Errorf("post-revival: CSRF store has no entry under rotated id %q: %v (RotateToken did not mint)", rotatedID, err)
	}

	// Assertion 3: CSRF store has NO token under the planted id. The
	// orphan T_planted_orphan_token_value must be gone, otherwise an
	// attacker who refreshed the planted-id token before the victim
	// authenticated retains a valid CSRF token bound to a now-stale
	// session id (the audited H-02 invariant).
	if got, err := csrfCfg.Store.Get(plantedID); err == nil {
		t.Errorf("post-revival: CSRF store still has token %q under planted id %q; RotateToken did not delete orphan", got, plantedID)
	}

	// Assertion 4: a follow-up request bearing the rotated cookie
	// reads user_id back via scheme.User. Proves the rotation
	// persisted across requests (Set-Cookie actually landed AND the
	// session bag rotated correctly).
	followReq, err := http.NewRequest(http.MethodGet, srv.URL+"/whoami", nil)
	if err != nil {
		t.Fatalf("NewRequest follow-up: %v", err)
	}
	followReq.AddCookie(rotatedCookie)
	followResp, err := client.Do(followReq)
	if err != nil {
		t.Fatalf("Do follow-up: %v", err)
	}
	defer followResp.Body.Close()
	if followResp.StatusCode != http.StatusOK {
		t.Fatalf("follow-up /whoami status = %d; want 200 (rotated cookie should persist user_id)", followResp.StatusCode)
	}
	var body struct {
		UserID string `json:"user_id"`
		Err    string `json:"err"`
	}
	if err := json.NewDecoder(followResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode follow-up body: %v", err)
	}
	if body.UserID != "u1" {
		t.Errorf("follow-up user_id = %q (err=%q); want u1", body.UserID, body.Err)
	}
}
