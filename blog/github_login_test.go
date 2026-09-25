package blog_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"goblog/auth"
	"goblog/blog"
)

// The login page used to build GitHub's authorize URL in an inline script,
// concatenating window.location into redirect_uri (#631). It is built in Go
// now, and carries a state that ties the request to this browser (#637).

func TestGithubAuthorizeURL_HasAConstantRedirectAndAState(t *testing.T) {
	got := blog.GithubAuthorizeURL("https://example.com", "abc123", "st4te")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	q := u.Query()
	if len(q) != 3 {
		t.Errorf("authorize URL has %d parameters, want client_id, redirect_uri and state: %v", len(q), q)
	}
	// GitHub matches redirect_uri against the callback registered for the
	// app, so it must not vary with where the visitor was heading.
	if want := "https://example.com/login"; q.Get("redirect_uri") != want {
		t.Errorf("redirect_uri = %q, want the constant %q", q.Get("redirect_uri"), want)
	}
	if q.Get("state") != "st4te" {
		t.Errorf("state = %q, want it passed through", q.Get("state"))
	}
}

// TestGithubAuthorizeURL_EscapesItsParameters is the bug in #631: unescaped, a
// value containing & would end its parameter early and the rest would reach
// GitHub as further authorize parameters.
func TestGithubAuthorizeURL_EscapesItsParameters(t *testing.T) {
	got := blog.GithubAuthorizeURL("https://example.com", "id&injected=1", "st&ate")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	q := u.Query()
	if len(q) != 3 {
		t.Errorf("a value smuggled in an extra parameter: %v", q)
	}
	if q.Get("client_id") != "id&injected=1" || q.Get("state") != "st&ate" {
		t.Errorf("values did not survive escaping: client_id=%q state=%q", q.Get("client_id"), q.Get("state"))
	}
}

// newSessionRouter wires the cookie session store the real server uses, so
// these tests exercise the same Set/Save path as production.
func newSessionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("goblog", cookie.NewStore([]byte("test-session-key"))))
	return r
}

// startLogin runs GET /login/github and returns the redirect target and the
// cookies it set.
func startLogin(t *testing.T, target string) (string, []*http.Cookie) {
	t.Helper()
	t.Setenv("client_id", "abc123")
	b := blog.New(nil, nil, "test")
	r := newSessionRouter(t)
	r.GET("/login/github", b.GithubLogin)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	if w.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", w.Code)
	}
	return w.Header().Get("Location"), w.Result().Cookies()
}

func TestGithubLogin_SendsAStateAndKeepsItInTheSession(t *testing.T) {
	loc, cookies := startLogin(t, "/login/github?next=/admin/settings")

	u, _ := url.Parse(loc)
	if !strings.HasPrefix(loc, "https://github.com/login/oauth/authorize") {
		t.Fatalf("did not redirect to GitHub: %s", loc)
	}
	if u.Query().Get("state") == "" {
		t.Error("authorize URL carries no state, so the callback cannot tie the code to this browser")
	}
	// next is no longer in redirect_uri; it rides in the session.
	if strings.Contains(u.Query().Get("redirect_uri"), "next") {
		t.Errorf("redirect_uri should be constant, got %q", u.Query().Get("redirect_uri"))
	}
	if len(cookies) == 0 {
		t.Error("no session cookie set, so the state was never stored")
	}
}

// TestGithubLogin_StateDiffersEveryTime: a predictable state would defeat the
// point of having one.
func TestGithubLogin_StateDiffersEveryTime(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		loc, _ := startLogin(t, "/login/github")
		u, _ := url.Parse(loc)
		state := u.Query().Get("state")
		if len(state) < 20 {
			t.Fatalf("state %q is too short to be unguessable", state)
		}
		if seen[state] {
			t.Fatalf("state %q was issued twice", state)
		}
		seen[state] = true
	}
}

// TestGithubLogin_RejectsOffsiteNext: next becomes the post-login redirect, so
// an unchecked value here is an open redirect.
func TestGithubLogin_RejectsOffsiteNext(t *testing.T) {
	for _, next := range []string{"https://evil.example", "//evil.example", "/\\evil.example"} {
		loc, _ := startLogin(t, "/login/github?next="+url.QueryEscape(next))
		if strings.Contains(loc, "evil.example") {
			t.Errorf("next %q reached the authorize URL: %s", next, loc)
		}
	}
}

// TestRequestOrigin: the origin must be the host the visitor is on, because
// GitHub matches redirect_uri against the app's registered callback.
func TestRequestOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, forwarded, want string
	}{
		{"plain http", "example.com", "", "http://example.com"},
		{"behind a TLS proxy", "example.com", "https", "https://example.com"},
		{"proxy chain takes the first", "example.com", "https, http", "https://example.com"},
		{"case insensitive", "example.com", "HTTPS", "https://example.com"},
		{"host with a port", "localhost:7000", "", "http://localhost:7000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/login/github", nil)
			c.Request.Host = tc.host
			if tc.forwarded != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if got := blog.RequestOrigin(c); got != tc.want {
				t.Errorf("RequestOrigin = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOAuthSessionKeys guards the contract between the two packages: blog
// writes these, auth reads them.
func TestOAuthSessionKeys(t *testing.T) {
	if auth.OAuthStateKey == "" || auth.OAuthNextKey == "" || auth.OAuthStateKey == auth.OAuthNextKey {
		t.Errorf("session keys are not distinct and non-empty: %q %q", auth.OAuthStateKey, auth.OAuthNextKey)
	}
}
