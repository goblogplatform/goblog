package auth

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"goblog/mail"
)

// IAuth interface for auth so that it can be mocked easier
type IAuth interface {
	IsAdmin(c *gin.Context) bool
	IsLoggedIn(c *gin.Context) bool
	IsWizardMode(c *gin.Context) bool
	EmailLoginEnabled() bool
	CurrentUser(c *gin.Context) *BlogUser
	ListUsers(offset, limit int) ([]UserListing, int64)
	PromoteAdmin(userID int) error
	DemoteAdmin(userID int) error
}

// Auth API
type Auth struct {
	db      **gorm.DB // needs a double pointer to be able to update the db
	version string
	// Mailer delivers one-time login codes. nil means email login is not
	// configured and the OTP endpoints respond 404.
	Mailer mail.Sender
	// sendLimiter throttles POST /api/login/email per client IP. It is a
	// pointer (rather than an embedded sync.Mutex) because Auth is passed
	// around by value (New returns a value, wizard constructs its own); every
	// copy of a given Auth shares the same limiter.
	sendLimiter *ipLimiter
}

// New constructs an Auth API
func New(db *gorm.DB, version string) Auth {
	api := Auth{db: &db, version: version, sendLimiter: newIPLimiter()}
	return api
}

// EmailLoginEnabled reports whether email login is configured, i.e. whether
// the login page should offer email login. It mirrors the check the OTP
// handlers use (Mailer != nil), which main sets once at startup from the
// environment, so the login page and the endpoints can never disagree.
func (a *Auth) EmailLoginEnabled() bool {
	return a.Mailer != nil
}

// AccessTokenResponse comes from Github OAuth API when the user has successfully
// authenticated - note the github api provides more fields but we can just leave
// them out and it will parse just fine - cool!
type AccessTokenResponse struct {
	AccessToken string `json:"access_token"`
}

func (a *Auth) UpdateDb(db *gorm.DB) {
	a.db = &db
}

// use the Github app credentials + the code we received from javascript
// client side to make the access token (bearer) request
func (a *Auth) requestAccessToken(parsedCode string) (*AccessTokenResponse, error) {
	err := godotenv.Load(".env")
	if err != nil {
		return nil, errors.New("Error loading .env file: " + err.Error())
	}
	clientID := os.Getenv("client_id")
	clientSecret := os.Getenv("client_secret")

	data := &AccessTokenResponse{}

	//todo: move these out of the code and into environment variables
	formData := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {parsedCode},
	}
	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	bodyString := string(bodyBytes)

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(bodyString)
	}

	json.Unmarshal(bodyBytes, &data)
	return data, nil
}

// githubUser is the subset of GitHub's /user response we keep.
type githubUser struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Name      string `json:"name"`
	Email     string `json:"email"`
}

// RequestUser fetches the GitHub profile for accessToken and returns it as an
// unstored BlogUser (ID is zero; the database assigns it on UpsertUser).
func (a *Auth) RequestUser(accessToken string) (*BlogUser, error) {
	//get the user info from Github
	req, err := http.NewRequest("GET", "https://api.github.com/user", strings.NewReader(""))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+accessToken)

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	bodyString := string(bodyBytes)

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(bodyString)
	}

	user, err := parseGitHubUser(bodyBytes, accessToken)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// parseGitHubUser parses GitHub's /user response body into an unstored
// BlogUser. It rejects a body that doesn't unmarshal as JSON and one that
// unmarshals but carries no id (id 0 is not a real GitHub user id; treating
// it as one would let unrelated failures collapse onto a single shared
// identity).
func parseGitHubUser(body []byte, accessToken string) (*BlogUser, error) {
	gh := &githubUser{}
	if err := json.Unmarshal(body, gh); err != nil {
		return nil, errors.New("unexpected GitHub user response: " + err.Error())
	}
	if gh.ID == 0 {
		return nil, errors.New("unexpected GitHub user response: missing id")
	}
	return &BlogUser{
		Provider:    ProviderGitHub,
		ProviderID:  strconv.Itoa(gh.ID),
		Login:       gh.Login,
		AvatarURL:   gh.AvatarURL,
		Name:        gh.Name,
		Email:       gh.Email,
		AccessToken: accessToken,
	}, nil
}

// UpsertUser stores user, matching an existing row by (Provider, ProviderID).
// On a miss the row is created and the database assigns ID. On a hit the
// profile fields and AccessToken are refreshed. Either way the stored row is
// returned.
func (a *Auth) UpsertUser(user *BlogUser) (*BlogUser, error) {
	if user.Provider == "" || user.ProviderID == "" {
		return nil, errors.New("user must have a provider and provider id")
	}
	var existing BlogUser
	err := (*a.db).Where("provider = ? AND provider_id = ?", user.Provider, user.ProviderID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user.ID = 0
		if err := (*a.db).Create(user).Error; err != nil {
			return nil, err
		}
		return user, nil
	}
	if err != nil {
		return nil, err
	}
	existing.Login = user.Login
	existing.AvatarURL = user.AvatarURL
	existing.Name = user.Name
	existing.Email = user.Email
	existing.AccessToken = user.AccessToken
	// Save writes every column, so a field GitHub now hides (e.g. a private
	// email) is cleared rather than left stale.
	if err := (*a.db).Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// LoginPostHandler should be called with the code provided by github. After
// receiving the code, this will reach out to github to retrieve and auth token
// which is stored in the db along with the user information from github.
// this can then be used for authorization when the api user supplies the same
// auth token later on for API access. Only one auth token per user can be used
// at once. Logout should remove the auth token from the table.
func (a *Auth) LoginPostHandler(c *gin.Context) {
	parsedCode := c.PostForm("code")
	data, err := a.requestAccessToken(parsedCode)
	if err != nil {
		c.JSON(http.StatusUnauthorized, "Error requesting token access: "+err.Error())
		return
	}

	user, err := a.RequestUser(data.AccessToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, err.Error())
		return
	}

	stored, err := a.UpsertUser(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, "Error storing user: "+err.Error())
		return
	}

	// On a fresh install where the operator has pre-populated .env (e.g. via
	// configuration management), the install wizard's UI flow is bypassed and
	// no admin user is ever created. Promote the first successful OAuth login
	// to admin so the operator can administer the site without re-running the
	// wizard. Same trust model as the wizard: whoever first completes OAuth
	// against the server's client_secret becomes the admin.
	if err := a.EnsureAdmin(stored); errors.Is(err, ErrNotConfiguredAdmin) {
		log.Printf("%s (id %s) logged in but is not the configured admin; not promoting", stored.Login, stored.ProviderID)
	} else if err != nil {
		log.Println("Error ensuring admin user: " + err.Error())
	}

	//save the access token in the session
	session := sessions.Default(c)
	session.Set("token", data.AccessToken)
	session.Save()

	c.JSON(http.StatusOK, stored)
}

// ErrNotConfiguredAdmin is returned by EnsureAdmin when no admin exists yet
// but the logging-in user is not the identity pinned by admin_login /
// admin_github_id in .env.
var ErrNotConfiguredAdmin = errors.New("user is not the configured admin")

// isConfiguredAdmin reports whether user matches the admin identity pinned in
// .env. Only GitHub users can be admin. If neither admin_login nor
// admin_github_id is set, any GitHub user matches (first-to-login wins). If
// either is set, the user must match at least one: admin_login
// case-insensitively against the GitHub login, admin_github_id numerically
// against the GitHub id (ProviderID). A malformed admin_github_id is treated
// as set but never matching, so a typo cannot silently reopen the gate.
func isConfiguredAdmin(user *BlogUser) bool {
	if user.Provider != ProviderGitHub {
		return false
	}
	login := strings.TrimSpace(os.Getenv("admin_login"))
	idStr := strings.TrimSpace(os.Getenv("admin_github_id"))
	if login == "" && idStr == "" {
		return true
	}
	if login != "" && strings.EqualFold(login, user.Login) {
		return true
	}
	if idStr != "" {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			log.Printf("admin_github_id %q is not a number; no user can match it", idStr)
			return false
		}
		if githubID, err := strconv.Atoi(user.ProviderID); err == nil && githubID == id {
			return true
		}
	}
	return false
}

// EnsureAdmin promotes the given BlogUser to admin if and only if no admin
// exists yet and the user matches the identity pinned in .env (if any, see
// isConfiguredAdmin). Returns ErrNotConfiguredAdmin when promotion was refused
// because of that pin; callers can treat that as "log in as a regular user".
// Idempotent and safe to call on every login.
//
// The check-and-create runs inside a transaction so two concurrent first
// logins can't both observe an empty admin_users table and both create a
// row. Lookup errors other than "record not found" are surfaced rather
// than swallowed as if no admin existed.
func (a *Auth) EnsureAdmin(user *BlogUser) error {
	return (*a.db).Transaction(func(tx *gorm.DB) error {
		var existing AdminUser
		err := tx.First(&existing).Error
		if err == nil {
			return nil // an admin already exists; nothing to do
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !isConfiguredAdmin(user) {
			return ErrNotConfiguredAdmin
		}
		return tx.Create(&AdminUser{BlogUserID: user.ID}).Error
	})
}

// ErrNotPromotable is returned by PromoteAdmin for users who can't hold
// admin: only GitHub users can (email login is a weaker credential, see #565).
var ErrNotPromotable = errors.New("only GitHub users can be admin")

// ErrLastAdmin is returned by DemoteAdmin when the demotion would leave the
// site with no admin at all, which would also re-open the install wizard
// (IsWizardMode is "no admin_users row").
var ErrLastAdmin = errors.New("cannot demote the last admin")

// UserListing is a BlogUser with its admin status, for the admin users page.
type UserListing struct {
	BlogUser
	IsAdmin bool
}

// ListUsers returns a page of users, newest first, each flagged with whether
// they are currently admin, along with the total number of users.
func (a *Auth) ListUsers(offset, limit int) ([]UserListing, int64) {
	var total int64
	(*a.db).Model(&BlogUser{}).Count(&total)
	var users []BlogUser
	(*a.db).Order("id desc").Offset(offset).Limit(limit).Find(&users)

	var admins []AdminUser
	(*a.db).Find(&admins)
	isAdmin := make(map[int]bool, len(admins))
	for _, admin := range admins {
		isAdmin[admin.BlogUserID] = true
	}

	listing := make([]UserListing, len(users))
	for i, u := range users {
		listing[i] = UserListing{BlogUser: u, IsAdmin: isAdmin[u.ID]}
	}
	return listing, total
}

// PromoteAdmin makes the given user an admin. Unlike EnsureAdmin this is not
// gated on the admin_login / admin_github_id pin: that pin only governs who
// bootstraps the first admin, after which existing admins decide. Returns
// gorm.ErrRecordNotFound for an unknown user and ErrNotPromotable for a
// non-GitHub user. Idempotent.
func (a *Auth) PromoteAdmin(userID int) error {
	return (*a.db).Transaction(func(tx *gorm.DB) error {
		var user BlogUser
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.Provider != ProviderGitHub {
			return ErrNotPromotable
		}
		var existing AdminUser
		err := tx.Where("blog_user_id = ?", userID).First(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&AdminUser{BlogUserID: userID}).Error
	})
}

// DemoteAdmin removes the given user's admin status. Returns
// gorm.ErrRecordNotFound if they are not an admin and ErrLastAdmin if they
// are the only one. The count-and-delete runs in a transaction so two
// concurrent demotions can't both see two admins and remove both.
func (a *Auth) DemoteAdmin(userID int) error {
	return (*a.db).Transaction(func(tx *gorm.DB) error {
		var existing AdminUser
		if err := tx.Where("blog_user_id = ?", userID).First(&existing).Error; err != nil {
			return err
		}
		var total int64
		if err := tx.Model(&AdminUser{}).Count(&total).Error; err != nil {
			return err
		}
		if total <= 1 {
			return ErrLastAdmin
		}
		return tx.Where("blog_user_id = ?", userID).Delete(&AdminUser{}).Error
	})
}

// DisplayUserTable is a debug function, shows the user table
func (a *Auth) DisplayUserTable() {
	var users []BlogUser
	(*a.db).Find(&users)
	log.Println(users)
}

// IsAdmin returns true if the user logged in is the admin user.
// First tries for a session token, and if that fails falls back on an auth token.
//
// Returns false when no admin user exists in the DB. Pre-admin handlers that
// the install wizard genuinely needs (e.g. UploadFile, UpdateSettings) should
// also accept IsWizardMode as an exemption.
func (a *Auth) IsAdmin(c *gin.Context) bool {
	var adminUser AdminUser
	err := (*a.db).First(&adminUser).Error
	if err != nil {
		return false
	}

	session := sessions.Default(c)
	token := session.Get("token")
	if token == nil {
		token = c.Request.Header.Get("Authorization")
		if token == "" {
			return false
		}
	}

	// first make sure the access token matches a logged in user
	var existingUser BlogUser
	err = (*a.db).Where("access_token = ?", token).First(&existingUser).Error
	if err != nil {
		return false
	}
	// next make sure the logged in user is an admin
	err = (*a.db).Where("blog_user_id = ?", existingUser.ID).First(&adminUser).Error
	if err != nil {
		return false
	}
	return true
}

// IsWizardMode returns true when the install wizard has not yet completed,
// detected by the absence of any row in the admin_users table. The wizard's
// own pre-admin endpoints (image upload, initial settings) gate on this so
// that fresh-install setup can complete before an admin user exists, without
// IsAdmin itself being permissive to anonymous traffic.
func (a *Auth) IsWizardMode(c *gin.Context) bool {
	var adminUser AdminUser
	err := (*a.db).First(&adminUser).Error
	return err != nil
}

// CurrentUser returns the user whose session token is in the request's
// session, or nil when nobody is logged in (no token, or a token that no
// longer matches a user).
func (a *Auth) CurrentUser(c *gin.Context) *BlogUser {
	session := sessions.Default(c)
	token := session.Get("token")
	if token == nil {
		return nil
	}
	var existingUser BlogUser
	err := (*a.db).Where("access_token = ?", token).First(&existingUser).Error
	if err != nil {
		return nil
	}
	return &existingUser
}

// IsLoggedIn Returns true if the user is logged in, false otherwise
func (a *Auth) IsLoggedIn(c *gin.Context) bool {
	return a.CurrentUser(c) != nil
}
