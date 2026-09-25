package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// GithubCallback must not exchange a code unless the state comes back exactly
// as it was issued. Without that, a code obtained for one account can be
// replayed into another visitor's browser and leave them signed in as
// somebody else (#637).
//
// These are internal tests so they can point githubTokenURL at a stand-in.
// That matters: asserting only that no session was created would pass even
// with the state check removed, because an exchange that never reaches GitHub
// fails anyway. What distinguishes a rejected callback from a failed one is
// whether the token endpoint was called at all.

type fakeGithub struct {
	tokenCalls int
	userCalls  int
	server     *httptest.Server
}

func newFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	f := &fakeGithub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-from-github", "token_type": "bearer"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		f.userCalls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "octocat", "name": "Octo Cat", "email": "octo@example.invalid"})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	oldToken, oldUser := githubTokenURL, githubUserURL
	githubTokenURL, githubUserURL = f.server.URL+"/token", f.server.URL+"/user"
	t.Cleanup(func() { githubTokenURL, githubUserURL = oldToken, oldUser })
	return f
}

func callbackRouter(t *testing.T) (*gin.Engine, *fakeGithub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("client_id", "id")
	t.Setenv("client_secret", "secret")

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&BlogUser{}, &AdminUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a := New(db, "test")
	fake := newFakeGithub(t)

	r := gin.New()
	r.Use(sessions.Sessions("goblog", cookie.NewStore([]byte("test-session-key"))))
	r.GET("/seed", func(c *gin.Context) { // what /login/github does
		s := sessions.Default(c)
		s.Set(OAuthStateKey, c.Query("state"))
		s.Set(OAuthNextKey, "/admin/settings")
		if err := s.Save(); err != nil {
			t.Fatalf("seeding the session: %v", err)
		}
		c.Status(http.StatusOK)
	})
	r.GET("/login", a.GithubCallback)
	r.GET("/whoami", func(c *gin.Context) {
		token, _ := sessions.Default(c).Get("token").(string)
		c.String(http.StatusOK, token)
	})
	return r, fake
}

func seed(t *testing.T, r *gin.Engine, state string) []*http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/seed?state="+state, nil))
	return w.Result().Cookies()
}

func get(t *testing.T, r *gin.Engine, target string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sessionToken(t *testing.T, r *gin.Engine, cookies []*http.Cookie) string {
	t.Helper()
	return get(t, r, "/whoami", cookies).Body.String()
}

// TestGithubCallback_HappyPath pins what a real login does, so the rejection
// tests below mean something: on a matching state the code IS exchanged and
// the visitor lands on the next they asked for.
func TestGithubCallback_HappyPath(t *testing.T) {
	r, fake := callbackRouter(t)
	cookies := seed(t, r, "matching-state")

	w := get(t, r, "/login?code=good-code&state=matching-state", cookies)

	if fake.tokenCalls != 1 {
		t.Errorf("token endpoint called %d times, want 1", fake.tokenCalls)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/settings" {
		t.Errorf("Location = %q, want the stored next", loc)
	}
	if token := sessionToken(t, r, w.Result().Cookies()); token != "tok-from-github" {
		t.Errorf("session token = %q, want the one GitHub returned", token)
	}
}

func TestGithubCallback_MismatchedStateNeverReachesGithub(t *testing.T) {
	r, fake := callbackRouter(t)
	cookies := seed(t, r, "the-real-state")

	w := get(t, r, "/login?code=attacker-code&state=not-the-real-state", cookies)

	if fake.tokenCalls != 0 {
		t.Errorf("the code was exchanged despite a mismatched state (%d calls)", fake.tokenCalls)
	}
	if loc := w.Header().Get("Location"); loc != "/login?error=github" {
		t.Errorf("Location = %q, want /login?error=github", loc)
	}
	if token := sessionToken(t, r, w.Result().Cookies()); token != "" {
		t.Errorf("a mismatched state logged someone in (token %q)", token)
	}
}

// TestGithubCallback_NoLoginStartedNeverReachesGithub is the replay this
// exists to stop: a browser that never visited /login/github has no stored
// state, so a code handed to it must be ignored.
func TestGithubCallback_NoLoginStartedNeverReachesGithub(t *testing.T) {
	r, fake := callbackRouter(t)

	w := get(t, r, "/login?code=attacker-code&state=anything", nil)

	if fake.tokenCalls != 0 {
		t.Errorf("a code was exchanged without a login having been started (%d calls)", fake.tokenCalls)
	}
	if token := sessionToken(t, r, w.Result().Cookies()); token != "" {
		t.Errorf("session created anyway (token %q)", token)
	}
}

// TestGithubCallback_EmptyStateIsNeverTreatedAsAMatch: two empty strings
// compare equal, so a browser that stored nothing receiving a callback with no
// state must not be read as a match. That case restarts the flow rather than
// being rejected (see the restart tests below), but either way the unvalidated
// code must not be exchanged — which is what this pins.
func TestGithubCallback_EmptyStateIsNeverTreatedAsAMatch(t *testing.T) {
	r, fake := callbackRouter(t)

	w := get(t, r, "/login?code=attacker-code", nil)

	if fake.tokenCalls != 0 {
		t.Errorf("empty state treated as a match (%d calls)", fake.tokenCalls)
	}
	if token := sessionToken(t, r, w.Result().Cookies()); token != "" {
		t.Errorf("a session was created from it (token %q)", token)
	}
}

// TestGithubCallback_StateIsSpentAfterOneUse: a state is cleared on use, so
// the same code and state cannot be presented twice.
func TestGithubCallback_StateIsSpentAfterOneUse(t *testing.T) {
	r, fake := callbackRouter(t)
	cookies := seed(t, r, "one-shot")

	first := get(t, r, "/login?code=good-code&state=one-shot", cookies)
	if fake.tokenCalls != 1 {
		t.Fatalf("first use: token calls = %d, want 1", fake.tokenCalls)
	}

	second := get(t, r, "/login?code=good-code&state=one-shot", first.Result().Cookies())
	if fake.tokenCalls != 1 {
		t.Errorf("the state was usable a second time (token calls = %d)", fake.tokenCalls)
	}
	if loc := second.Header().Get("Location"); loc != "/login?error=github" {
		t.Errorf("Location = %q, want the replay rejected", loc)
	}
}

func TestNewOAuthState(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		s, err := NewOAuthState()
		if err != nil {
			t.Fatalf("NewOAuthState: %v", err)
		}
		if len(s) < 40 {
			t.Fatalf("state %q is shorter than 32 random bytes would give", s)
		}
		if seen[s] {
			t.Fatalf("state %q was issued twice", s)
		}
		seen[s] = true
	}
}

// A login page from before goblog took the OAuth flow over builds the
// authorize URL itself and asks for no state, so GitHub returns without one.
// Rejecting that would strand the admin: updating the theme needs a login, and
// the login is what is broken. Those callbacks restart the flow instead (#637
// follow-up).

func TestGithubCallback_NoStateAtAllRestartsTheFlow(t *testing.T) {
	r, fake := callbackRouter(t)

	w := get(t, r, "/login?code=code-from-an-old-theme", nil)

	if loc := w.Header().Get("Location"); loc != "/login/github" {
		t.Errorf("Location = %q, want a restart at /login/github", loc)
	}
	// The code is discarded, not exchanged: it was never tied to this browser.
	if fake.tokenCalls != 0 {
		t.Errorf("the unvalidated code was exchanged (%d calls)", fake.tokenCalls)
	}
	if token := sessionToken(t, r, w.Result().Cookies()); token != "" {
		t.Errorf("a session was created from an unvalidated code (token %q)", token)
	}
}

// TestGithubCallback_RestartKeepsNext: the old flow put next in redirect_uri,
// so it is on the callback URL. Carrying it over means the restart lands where
// the visitor was originally going.
func TestGithubCallback_RestartKeepsNext(t *testing.T) {
	r, _ := callbackRouter(t)

	w := get(t, r, "/login?code=c&next=%2Fadmin%2Fsettings", nil)

	if loc := w.Header().Get("Location"); loc != "/login/github?next=%2Fadmin%2Fsettings" {
		t.Errorf("Location = %q, want next carried into the restart", loc)
	}
}

// TestGithubCallback_RestartCannotLoop is the property that makes restarting
// safe: /login/github always sends a state, so a browser whose cookies do not
// survive comes back *with* a state and gets an error rather than another
// restart. Only a stateless callback restarts, and only our own flow is
// stateful, so the two cannot alternate.
func TestGithubCallback_RestartCannotLoop(t *testing.T) {
	r, fake := callbackRouter(t)

	// A callback carrying a state, but from a browser that stored nothing —
	// what the restarted flow looks like when cookies are not kept.
	w := get(t, r, "/login?code=c&state=issued-but-never-stored", nil)

	if loc := w.Header().Get("Location"); loc == "/login/github" {
		t.Error("a stateful callback restarted the flow, which is how a cycle would form")
	}
	if loc := w.Header().Get("Location"); loc != "/login?error=github" {
		t.Errorf("Location = %q, want the error page", loc)
	}
	if fake.tokenCalls != 0 {
		t.Errorf("exchanged anyway (%d calls)", fake.tokenCalls)
	}
}
