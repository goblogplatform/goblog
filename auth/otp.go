package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
)

// normalizeEmail trims and lowercases raw and does a shape check: non-empty,
// at most 254 bytes, exactly one "@" with something on both sides. It is
// deliberately loose; the emailed code is the real proof of ownership.
func normalizeEmail(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 {
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
// the same address get 429.
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
	now := time.Now()
	(*a.db).Where("expires_at < ?", now).Delete(&LoginCode{})

	var existing LoginCode
	if err := (*a.db).First(&existing, "email = ?", email).Error; err == nil && now.Sub(existing.CreatedAt) < loginCodeResendAfter {
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
		(*a.db).Delete(&LoginCode{}, "email = ?", email)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

// VerifyLoginCodeHandler handles POST /api/login/email/verify. Implemented in
// the next step of the plan.
func (a *Auth) VerifyLoginCodeHandler(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "not implemented"})
}
