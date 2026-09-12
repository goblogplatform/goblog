package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"goblog/auth"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// fakeMailer records sends and can be told to fail.
type fakeMailer struct {
	sent []sentMail
	err  error
}

type sentMail struct{ to, subject, body string }

func (f *fakeMailer) Send(to, subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentMail{to, subject, body})
	return nil
}

var codeRe = regexp.MustCompile(`\b[0-9]{6}\b`)

// lastCode pulls the 6-digit code out of the most recent email.
func lastCode(t *testing.T, m *fakeMailer) string {
	t.Helper()
	if len(m.sent) == 0 {
		t.Fatal("no email was sent")
	}
	code := codeRe.FindString(m.sent[len(m.sent)-1].body)
	if code == "" {
		t.Fatalf("no 6-digit code in body %q", m.sent[len(m.sent)-1].body)
	}
	return code
}

// newOTPRouter mounts the OTP handlers plus a /whoami probe that reports
// IsLoggedIn for the request's session cookie.
func newOTPRouter(a *auth.Auth) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte("test"))))
	r.POST("/api/login/email", a.SendLoginCodeHandler)
	r.POST("/api/login/email/verify", a.VerifyLoginCodeHandler)
	r.GET("/whoami", func(c *gin.Context) { c.String(http.StatusOK, strconv.FormatBool(a.IsLoggedIn(c))) })
	return r
}

func postForm(r *gin.Engine, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sendCode(r *gin.Engine, email string) *httptest.ResponseRecorder {
	return postForm(r, "/api/login/email", url.Values{"email": {email}}, nil)
}

// newOTPAuth is newAuth with a fake mailer attached.
func newOTPAuth(t *testing.T) (*auth.Auth, *gorm.DB, *fakeMailer) {
	t.Helper()
	a, db := newAuth(t)
	m := &fakeMailer{}
	a.Mailer = m
	return a, db, m
}

func codeCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	db.Model(&auth.LoginCode{}).Count(&n)
	return n
}

func TestSendLoginCode_NotConfigured_404(t *testing.T) {
	a, _ := newAuth(t)
	w := sendCode(newOTPRouter(a), "x@example.com")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without a mailer, got %d %s", w.Code, w.Body)
	}
}

func TestSendLoginCode_BadEmail_400(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	for _, bad := range []string{"", "   ", "no-at-sign", "@example.com", "x@", "a@b@c", strings.Repeat("a", 250) + "@x.io"} {
		w := sendCode(r, bad)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: expected 400, got %d %s", bad, w.Code, w.Body)
		}
	}
	if len(m.sent) != 0 || codeCount(t, db) != 0 {
		t.Fatal("nothing should be sent or stored for invalid addresses")
	}
}

func TestSendLoginCode_SendsCodeAndStoresHash(t *testing.T) {
	a, db, m := newOTPAuth(t)
	db.Exec("CREATE TABLE settings (key text PRIMARY KEY, type text, value text)")
	db.Exec("INSERT INTO settings (key, type, value) VALUES ('site_title', 'text', 'My Blog')")

	w := sendCode(newOTPRouter(a), "  Reader@Example.COM ")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sent"`) {
		t.Fatalf("expected 200 sent, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected 1 email, got %d", len(m.sent))
	}
	if m.sent[0].to != "reader@example.com" {
		t.Errorf("expected normalised recipient, got %q", m.sent[0].to)
	}
	if m.sent[0].subject != "Your My Blog login code" {
		t.Errorf("unexpected subject %q", m.sent[0].subject)
	}
	code := lastCode(t, m)
	if !strings.Contains(m.sent[0].body, "expires in 10 minutes") {
		t.Errorf("body should mention expiry: %q", m.sent[0].body)
	}

	var row auth.LoginCode
	if err := db.First(&row, "email = ?", "reader@example.com").Error; err != nil {
		t.Fatalf("expected a login_codes row: %v", err)
	}
	if row.CodeHash == code || len(row.CodeHash) != 64 {
		t.Errorf("code must be stored as a sha256 hex hash, got %q", row.CodeHash)
	}
	if row.Attempts != 0 {
		t.Errorf("attempts should start at 0, got %d", row.Attempts)
	}
	if until := time.Until(row.ExpiresAt); until < 9*time.Minute || until > 11*time.Minute {
		t.Errorf("expected ~10 minute expiry, got %v", until)
	}
}

func TestSendLoginCode_SiteTitleFallsBackToGoBlog(t *testing.T) {
	a, _, m := newOTPAuth(t) // no settings table in newAuth
	sendCode(newOTPRouter(a), "reader@example.com")
	if m.sent[0].subject != "Your GoBlog login code" {
		t.Errorf("unexpected subject %q", m.sent[0].subject)
	}
}

func TestSendLoginCode_SameResponseForKnownAndUnknownAddress(t *testing.T) {
	a, db, _ := newOTPAuth(t)
	db.Create(&auth.BlogUser{Provider: auth.ProviderEmail, ProviderID: "known@example.com", Login: "known@example.com"})
	r := newOTPRouter(a)
	known := sendCode(r, "known@example.com")
	unknown := sendCode(r, "unknown@example.com")
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("responses differ: %d %s vs %d %s", known.Code, known.Body, unknown.Code, unknown.Body)
	}
}

func TestSendLoginCode_RateLimited_429(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusOK {
		t.Fatalf("first send: %d %s", w.Code, w.Body)
	}
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 inside the resend window, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected the second request not to send, got %d emails", len(m.sent))
	}

	db.Model(&auth.LoginCode{}).Where("email = ?", "reader@example.com").Update("created_at", time.Now().Add(-2*time.Minute))
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusOK {
		t.Fatalf("expected 200 after the window, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 2 || codeCount(t, db) != 1 {
		t.Fatalf("expected a replacement code (2 emails, 1 row), got %d emails, %d rows", len(m.sent), codeCount(t, db))
	}
}

func TestSendLoginCode_PrunesExpiredRows(t *testing.T) {
	a, db, _ := newOTPAuth(t)
	db.Create(&auth.LoginCode{Email: "old@example.com", CodeHash: "x", ExpiresAt: time.Now().Add(-time.Hour), CreatedAt: time.Now().Add(-2 * time.Hour)})
	sendCode(newOTPRouter(a), "new@example.com")
	var n int64
	db.Model(&auth.LoginCode{}).Where("email = ?", "old@example.com").Count(&n)
	if n != 0 {
		t.Fatal("expected the expired row to be pruned")
	}
}

func TestSendLoginCode_MailFailure_500AndNoRow(t *testing.T) {
	a, db, m := newOTPAuth(t)
	m.err = errors.New("smtp down")
	w := sendCode(newOTPRouter(a), "reader@example.com")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "smtp down") {
		t.Errorf("mail error must not leak to the client: %s", w.Body)
	}
	if codeCount(t, db) != 0 {
		t.Fatal("expected no login_codes row after a failed send")
	}
}

func TestEmailLoginEnabled(t *testing.T) {
	t.Setenv("smtp_host", "")
	t.Setenv("smtp_from", "")
	if auth.EmailLoginEnabled() {
		t.Fatal("expected disabled without smtp_host/smtp_from")
	}
	t.Setenv("smtp_host", "smtp.example.com")
	t.Setenv("smtp_from", "blog@example.com")
	if !auth.EmailLoginEnabled() {
		t.Fatal("expected enabled")
	}
}
