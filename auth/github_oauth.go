package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"net/http"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

// GitHub's OAuth flow used to finish in the browser: the login page read
// ?code= out of the query and posted it to /api/login, which exchanged it and
// set the session. Nothing tied the code to the visitor who started the flow,
// so a code obtained for one account could be replayed into another visitor's
// browser and leave them signed in as somebody else (#637).
//
// The flow is now server-side end to end. /login/github mints a state, keeps
// it with the intended destination in the session, and sends it to GitHub;
// GithubCallback will not exchange a code unless the state comes back intact.

const (
	// OAuthStateKey and OAuthNextKey name the session values that carry a
	// login across the round trip to GitHub. blog sets them when it starts
	// the flow; this package reads them when GitHub returns.
	OAuthStateKey = "oauth_state"
	OAuthNextKey  = "oauth_next"
)

// NewOAuthState returns an unguessable value to tie an authorize request to
// the browser that made it.
func NewOAuthState() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// GithubCallback handles GitHub's return to /login.
//
// It answers with a redirect either way, so a failure leaves the visitor on
// the login page rather than on a dead end. ?error=github tells the template
// something went wrong; a theme that does not render it simply shows the
// login page again.
func (a *Auth) GithubCallback(c *gin.Context) {
	session := sessions.Default(c)

	want, _ := session.Get(OAuthStateKey).(string)
	next, _ := session.Get(OAuthNextKey).(string)

	// One shot: whether or not it matches, this state is spent. Every path
	// below saves, so a rejected callback still clears it and the same code
	// and state cannot be presented twice.
	session.Delete(OAuthStateKey)
	session.Delete(OAuthNextKey)

	reject := func(reason string) {
		log.Println("OAuth callback: " + reason)
		if err := session.Save(); err != nil {
			log.Println("OAuth callback: couldn't clear the login state: " + err.Error())
		}
		c.Redirect(http.StatusFound, "/login?error=github")
	}

	got := c.Query("state")
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		// Either this browser never started a login, or somebody else's code
		// is being handed to it. Send them back to start their own.
		reject("state did not match the session; ignoring the code")
		return
	}

	code := c.Query("code")
	if code == "" {
		reject("no code in the callback")
		return
	}

	// completeGithubLogin puts the token on the session; the single save
	// below persists it together with the cleared state.
	if err := a.completeGithubLogin(c, code); err != nil {
		reject(err.Error())
		return
	}
	if err := session.Save(); err != nil {
		log.Println("OAuth callback: couldn't save the session: " + err.Error())
		c.Redirect(http.StatusFound, "/login?error=github")
		return
	}

	if next == "" {
		next = "/"
	}
	c.Redirect(http.StatusFound, next)
}
