package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// One-time email login codes (issue #523). A visitor asks for a code, we
// email a 6-digit number and store only its hash; presenting the right code
// within the lifetime logs them in as an email-provider BlogUser.
const (
	loginCodeLength      = 6
	loginCodeLifetime    = 10 * time.Minute
	loginCodeMaxAttempts = 5
	loginCodeResendAfter = 60 * time.Second
	sessionTokenBytes    = 32

	// Per-IP limit on POST /api/login/email, independent of the per-address
	// resend window above: without it, one client can mail-bomb unlimited
	// distinct addresses or burn the site's SMTP quota.
	loginCodeSendsPerIP  = 5
	loginCodeSendsWindow = 10 * time.Minute
)

// ipLimiter counts recent hits per key (client IP) inside a sliding window.
// It is referenced from Auth via a pointer (see Auth.sendLimiter) because
// Auth values are copied around, and every copy must share one limiter.
type ipLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{hits: make(map[string][]time.Time)}
}

// allow reports whether ip may make another request at now, recording the
// attempt if so. Timestamps older than loginCodeSendsWindow are pruned first,
// so the limit only ever reflects the trailing window.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-loginCodeSendsWindow)
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginCodeSendsPerIP {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}

// emailRejectedChars are characters that have no place in a bare address but
// that a mail header/relay could interpret specially (e.g. "a@b.com,c@d.com"
// or "<a@b.com>" smuggling a second recipient). Rejecting them here is
// defence in depth; mail.Message separately rejects raw line breaks.
const emailRejectedChars = " \t,<>;\"()"

// normalizeEmail trims and lowercases raw and does a shape check: non-empty,
// at most 254 bytes, exactly one "@" with something on both sides, and none
// of emailRejectedChars. It is deliberately loose beyond that; the emailed
// code is the real proof of ownership.
func normalizeEmail(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 {
		return "", false
	}
	if strings.ContainsAny(e, emailRejectedChars) {
		return "", false
	}
	at := strings.Index(e, "@")
	if at <= 0 || at != strings.LastIndex(e, "@") || at == len(e)-1 {
		return "", false
	}
	return e, true
}

func generateLoginCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashLoginCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// siteTitle reads the site_title setting for the email subject. auth cannot
// import blog (blog imports auth), so it queries the settings table directly
// and falls back to "GoBlog" when the table or value is missing.
func (a *Auth) siteTitle() string {
	var title string
	err := (*a.db).Table("settings").Where("key = ?", "site_title").Select("value").Scan(&title).Error
	if err != nil || strings.TrimSpace(title) == "" {
		return "GoBlog"
	}
	return title
}

// SendLoginCodeHandler handles POST /api/login/email (form field: email).
// It always answers 200 {"status":"sent"} for a well-formed address whether
// or not that address has logged in before, so it cannot be used to probe
// for accounts. Requests inside loginCodeResendAfter of the previous one for
// the same address get 429, as do clients over loginCodeSendsPerIP sends in
// loginCodeSendsWindow (checked first, so it isn't itself an oracle for
// whether a given address was rate-limited).
func (a *Auth) SendLoginCodeHandler(c *gin.Context) {
	if a.Mailer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email login not configured"})
		return
	}
	email, ok := normalizeEmail(c.PostForm("email"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email address"})
		return
	}
	if !a.sendLimiter.allow(c.ClientIP(), time.Now()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests; try again later"})
		return
	}
	now := time.Now()
	(*a.db).Where("expires_at < ?", now).Delete(&LoginCode{})

	var existing LoginCode
	err := (*a.db).First(&existing, "email = ?", email).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("looking up login code for %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	if err == nil && now.Sub(existing.CreatedAt) < loginCodeResendAfter {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "please wait before requesting another code"})
		return
	}

	code, err := generateLoginCode()
	if err != nil {
		log.Printf("generating login code: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	row := LoginCode{Email: email, CodeHash: hashLoginCode(code), ExpiresAt: now.Add(loginCodeLifetime), Attempts: 0, CreatedAt: now}
	if err := (*a.db).Save(&row).Error; err != nil {
		log.Printf("storing login code for %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}

	subject := fmt.Sprintf("Your %s login code", a.siteTitle())
	body := fmt.Sprintf("Your login code is %s. It expires in 10 minutes.\n\nIf you did not request this, ignore this email.\n", code)
	if err := a.Mailer.Send(email, subject, body); err != nil {
		log.Printf("sending login code to %s: %v", email, err)
		if delErr := (*a.db).Delete(&LoginCode{}, "email = ?", email).Error; delErr != nil {
			log.Printf("cleaning up login code for %s after a failed send: %v", email, delErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

func isLoginCode(s string) bool {
	if len(s) != loginCodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func newSessionToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// VerifyLoginCodeHandler handles POST /api/login/email/verify (form fields:
// email, code). A missing, expired, locked or wrong code is a 401 with the
// same body. Success deletes the code, creates or refreshes the email user
// with a fresh random session token, and stores that token in the session
// exactly as the GitHub flow does, so IsLoggedIn works unchanged. Admin
// promotion is deliberately not attempted here.
func (a *Auth) VerifyLoginCodeHandler(c *gin.Context) {
	if a.Mailer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email login not configured"})
		return
	}
	email, ok := normalizeEmail(c.PostForm("email"))
	code := strings.TrimSpace(c.PostForm("code"))
	if !ok || !isLoginCode(code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email or code"})
		return
	}

	var row LoginCode
	err := (*a.db).First(&row, "email = ?", email).Error
	if err != nil || time.Now().After(row.ExpiresAt) || row.Attempts >= loginCodeMaxAttempts {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(hashLoginCode(code)), []byte(row.CodeHash)) != 1 {
		if err := (*a.db).Model(&row).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error; err != nil {
			log.Printf("recording a failed login attempt for %s: %v", email, err)
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}
	if err := (*a.db).Delete(&LoginCode{}, "email = ?", email).Error; err != nil {
		log.Printf("deleting used login code for %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}

	token, err := newSessionToken()
	if err != nil {
		log.Printf("generating session token: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}
	user, err := a.UpsertUser(&BlogUser{
		Provider:    ProviderEmail,
		ProviderID:  email,
		Login:       email,
		Email:       email,
		AccessToken: token,
	})
	if err != nil {
		log.Printf("storing email user %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}

	session := sessions.Default(c)
	session.Set("token", token)
	if err := session.Save(); err != nil {
		log.Printf("saving session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}
	c.JSON(http.StatusOK, user)
}
