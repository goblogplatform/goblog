package auth

import (
	"encoding/json"
	"errors"
	"fmt"
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
)

// IAuth interface for auth so that it can be mocked easier
type IAuth interface {
	IsAdmin(c *gin.Context) bool
	IsLoggedIn(c *gin.Context) bool
	IsWizardMode(c *gin.Context) bool
}

// Auth API
type Auth struct {
	db      **gorm.DB // needs a double pointer to be able to update the db
	version string
}

// New constructs an Auth API
func New(db *gorm.DB, version string) Auth {
	api := Auth{&db, version}
	return api
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
	fmt.Println("post:\n", bodyString) //todo: remove - just for debugging

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(bodyString)
	}

	json.Unmarshal(bodyBytes, &data)
	return data, nil
}

// formatRequest generates ascii representation of a request
func (a *Auth) formatRequest(r *http.Request) string {
	// Create return string
	var request []string // Add the request string
	url := fmt.Sprintf("%v %v %v", r.Method, r.URL, r.Proto)
	request = append(request, url)                             // Add the host
	request = append(request, fmt.Sprintf("Host: %v", r.Host)) // Loop through headers
	for name, headers := range r.Header {
		name = strings.ToLower(name)
		for _, h := range headers {
			request = append(request, fmt.Sprintf("%v: %v", name, h))
		}
	}

	// If this is a POST, add post data
	if r.Method == "POST" {
		r.ParseForm()
		request = append(request, "\n")
		request = append(request, r.Form.Encode())
	} // Return the request as a string
	return strings.Join(request, "\n")
}

// todo: make these request functions generalized
func (a *Auth) RequestUser(accessToken string) (*BlogUser, error) {
	data := &BlogUser{}
	//get the user info from Github
	req, err := http.NewRequest("GET", "https://api.github.com/user", strings.NewReader(""))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+accessToken)
	fmt.Println("REQ" + a.formatRequest(req))

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
	fmt.Println("github response:\n", bodyString) //todo: remove - just for debugging

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(bodyString)
	}

	json.Unmarshal(bodyBytes, &data)
	data.AccessToken = accessToken

	fmt.Println("Parsed user: ", data)

	return data, nil
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

	//check if user exists, if not add them, if they do update access token
	var existingUser BlogUser
	err = (*a.db).Where("ID = ?", user.ID).First(&existingUser).Error
	if err != nil {
		(*a.db).Create(&user)
	} else {
		(*a.db).Model(&user).Where("ID = ?", user.ID).Updates(&user)
		existingUser = *user
	}

	// On a fresh install where the operator has pre-populated .env (e.g. via
	// configuration management), the install wizard's UI flow is bypassed and
	// no admin user is ever created. Promote the first successful OAuth login
	// to admin so the operator can administer the site without re-running the
	// wizard. Same trust model as the wizard: whoever first completes OAuth
	// against the server's client_secret becomes the admin.
	if err := a.EnsureAdmin(user); errors.Is(err, ErrNotConfiguredAdmin) {
		log.Printf("%s (id %d) logged in but is not the configured admin; not promoting", user.Login, user.ID)
	} else if err != nil {
		log.Println("Error ensuring admin user: " + err.Error())
	}

	//save the access token in the session
	session := sessions.Default(c)
	session.Set("token", data.AccessToken)
	session.Save()

	c.JSON(http.StatusOK, existingUser)
}

// ErrNotConfiguredAdmin is returned by EnsureAdmin when no admin exists yet
// but the logging-in user is not the identity pinned by admin_login /
// admin_github_id in .env.
var ErrNotConfiguredAdmin = errors.New("user is not the configured admin")

// isConfiguredAdmin reports whether user matches the admin identity pinned in
// .env. If neither admin_login nor admin_github_id is set, any user matches
// (first-to-login wins). If either is set, the user must match at least one:
// admin_login case-insensitively against the GitHub login, admin_github_id
// against the numeric GitHub id. A malformed admin_github_id is treated as set
// but never matching, so a typo cannot silently reopen the gate.
func isConfiguredAdmin(user *BlogUser) bool {
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
		if id == user.ID {
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

// IsLoggedIn Returns true if the user is logged in, false otherwise
func (a *Auth) IsLoggedIn(c *gin.Context) bool {
	session := sessions.Default(c)
	token := session.Get("token")
	if token == nil {
		return false
	}
	var existingUser BlogUser
	err := (*a.db).Where("access_token = ?", token).First(&existingUser).Error
	if err != nil {
		return false
	}
	return true
}
