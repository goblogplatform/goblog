package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSessionOptions(t *testing.T) {
	o := sessionOptions("")
	if !o.HttpOnly || !o.Secure || o.SameSite != http.SameSiteLaxMode || o.Path != "/" || o.MaxAge <= 0 {
		t.Errorf("default options: %+v", o)
	}
	if sessionOptions("false").Secure {
		t.Error("SESSION_SECURE=false should clear Secure")
	}
	if !sessionOptions("true").Secure {
		t.Error("SESSION_SECURE=true should keep Secure")
	}
}

func TestRequireJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requireJSON())
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.POST("/api/v1/posts", ok)
	r.DELETE("/api/v1/comments", ok)
	r.DELETE("/api/v1/plugins/:name", ok)
	r.POST("/api/v1/themes/activate", ok)
	r.POST("/api/v1/upload", ok)
	r.POST("/api/v1/directory/repos/:id/approve", ok)
	r.GET("/api/v1/posts", ok)
	r.POST("/wizard_db", ok)

	cases := []struct {
		name, method, path, contentType, body string
		want                                  int
	}{
		{"json accepted", "POST", "/api/v1/posts", "application/json", `{}`, 200},
		{"json with charset accepted", "POST", "/api/v1/posts", "application/json; charset=UTF-8", `{}`, 200},
		{"text/plain rejected", "DELETE", "/api/v1/comments", "text/plain", `{"id":1}`, 415},
		{"form rejected", "POST", "/api/v1/posts", "application/x-www-form-urlencoded", "a=b", 415},
		{"missing type with body rejected", "POST", "/api/v1/posts", "", `{}`, 415},
		{"no body allowed", "DELETE", "/api/v1/plugins/hello", "", "", 200},
		// A repo submitted publicly must not be approvable by a cross-site
		// form post against a logged-in admin: forms cannot send JSON.
		{"cross-site approve rejected", "POST", "/api/v1/directory/repos/1/approve", "application/x-www-form-urlencoded", "", 415},
		{"admin approve allowed", "POST", "/api/v1/directory/repos/1/approve", "application/json", "", 200},
		{"cross-site theme activate rejected", "POST", "/api/v1/themes/activate", "application/x-www-form-urlencoded", "name=x", 415},
		{"theme activate allowed", "POST", "/api/v1/themes/activate", "application/json", `{"name":"x"}`, 200},
		{"multipart upload allowed", "POST", "/api/v1/upload", "multipart/form-data; boundary=x", "--x--", 200},
		{"multipart elsewhere rejected", "POST", "/api/v1/posts", "multipart/form-data; boundary=x", "--x--", 415},
		{"GET untouched", "GET", "/api/v1/posts", "", "", 200},
		{"wizard untouched", "POST", "/wizard_db", "application/x-www-form-urlencoded", "a=b", 200},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

// TestNoRawOAuthCodeEndpoint: /api/login took an OAuth code as a form post and
// set the session from it, and requireJSON deliberately let it through, so a
// cross-site form could plant a session on a visitor. SameSite=Lax does not
// help — it governs sending a cookie cross-site, not setting one. GitHub
// returns to /login now and the server does the exchange itself, against a
// state it minted (#637), so nothing accepts a bare code any more.
func TestNoRawOAuthCodeEndpoint(t *testing.T) {
	source, err := os.ReadFile("goblog.go")
	if err != nil {
		t.Fatalf("read goblog.go: %v", err)
	}
	// The closing quote matters: "/api/login/email" does not contain
	// `"/api/login"`, so the OTP routes below are not caught by this.
	if strings.Contains(string(source), `"/api/login"`) {
		t.Error(`/api/login is routed again: it accepts an OAuth code from anywhere, which is the login CSRF #637 closed`)
	}
	// Asserted positively so the check above is demonstrably about the OAuth
	// route alone, and so removing it does not quietly take the OTP endpoints
	// with it: those are a different flow and still wanted.
	for _, keep := range []string{`"/api/login/email"`, `"/api/login/email/verify"`} {
		if !strings.Contains(string(source), keep) {
			t.Errorf("%s is no longer routed; email login needs it", keep)
		}
	}
}
