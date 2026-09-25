package blog_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"goblog/blog"
)

// The login page used to build GitHub's authorize URL in an inline script,
// concatenating window.location straight into redirect_uri. #631 moved it into
// Go so the escaping is done once and can be tested.

// redirectURI pulls redirect_uri back out of an authorize URL, decoded.
func redirectURI(t *testing.T, authorize string) string {
	t.Helper()
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	return u.Query().Get("redirect_uri")
}

func TestGithubAuthorizeURL_KeepsNextThroughTheRoundTrip(t *testing.T) {
	got := blog.GithubAuthorizeURL("https://example.com", "abc123", "/admin/settings")
	if want := "https://example.com/login?next=%2Fadmin%2Fsettings"; redirectURI(t, got) != want {
		t.Errorf("redirect_uri = %q, want %q", redirectURI(t, got), want)
	}
	// GitHub returns the visitor to redirect_uri with &code= appended, and
	// Login reads next from that query. Dropping it would send everyone to /.
	if !strings.Contains(redirectURI(t, got), "next=") {
		t.Error("redirect_uri must keep ?next=, it is how next survives the round trip")
	}
}

// TestGithubAuthorizeURL_EscapesAmpersandInNext is the bug in #631: unescaped,
// everything after the & in next reaches GitHub as further authorize
// parameters and redirect_uri is truncated.
func TestGithubAuthorizeURL_EscapesAmpersandInNext(t *testing.T) {
	got := blog.GithubAuthorizeURL("https://example.com", "abc123", "/search?q=a&b=c")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	q := u.Query()
	if len(q) != 2 {
		t.Errorf("authorize URL has %d parameters, want exactly client_id and redirect_uri: %v", len(q), q)
	}
	if want := "https://example.com/login?next=%2Fsearch%3Fq%3Da%26b%3Dc"; q.Get("redirect_uri") != want {
		t.Errorf("redirect_uri = %q, want %q", q.Get("redirect_uri"), want)
	}
	// The raw string must not carry a bare & or ? from next.
	raw := got[strings.Index(got, "redirect_uri="):]
	if strings.Contains(raw, "&b=c") || strings.Contains(raw, "?q=") {
		t.Errorf("next is not escaped inside redirect_uri: %s", raw)
	}
}

func TestGithubAuthorizeURL_OmitsEmptyNext(t *testing.T) {
	for _, next := range []string{"", "/"} {
		got := redirectURI(t, blog.GithubAuthorizeURL("https://example.com", "abc", next))
		if got != "https://example.com/login" {
			t.Errorf("next %q: redirect_uri = %q, want no query string", next, got)
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

// TestGithubLogin_RejectsOffsiteNext: next reaches redirect_uri, so an open
// redirect here would be handed to GitHub. SafeNext already guards Login; it
// has to guard this entry point too.
func TestGithubLogin_RejectsOffsiteNext(t *testing.T) {
	t.Setenv("client_id", "abc123")
	b := blog.New(nil, nil, "test")

	for _, next := range []string{"https://evil.example", "//evil.example", "/\\evil.example"} {
		router := gin.New()
		router.GET("/login/github", b.GithubLogin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login/github?next="+url.QueryEscape(next), nil))

		if w.Code != http.StatusFound {
			t.Fatalf("next %q: code = %d, want 302", next, w.Code)
		}
		got := redirectURI(t, w.Header().Get("Location"))
		if strings.Contains(got, "evil.example") {
			t.Errorf("next %q leaked into redirect_uri: %s", next, got)
		}
	}
}
