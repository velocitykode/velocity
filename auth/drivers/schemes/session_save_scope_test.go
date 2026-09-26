package schemes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// SessionFromContext answers only for a session that is saved when its
// scope ends: a holder WithSessionContext attached on its own caches the
// session but nothing saves it, so state written there would be lost.
func TestSessionFromContext_OnlyInsideASaveScope(t *testing.T) {
	sess := auth.NewSession("")
	r := WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))
	holder := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	holder.setSession(sess)

	if got := SessionFromContext(r.Context()); got != nil {
		t.Fatal("SessionFromContext answered for a holder no scope saves")
	}
	if got := SessionFromRequest(r); got != sess {
		t.Fatal("premise: the holder does not cache the session")
	}

	holder.markSaveScope()
	if got := SessionFromContext(r.Context()); got != sess {
		t.Fatal("SessionFromContext did not answer inside a save scope")
	}

	standalone := sessionContext(httptest.NewRequest(http.MethodPost, "/login", nil), sess)
	if got := SessionFromContext(standalone); got != sess {
		t.Fatal("SessionFromContext did not answer for a standalone operation's own scope")
	}
}
