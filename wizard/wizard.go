package wizard

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"goblog/auth"
	"gorm.io/gorm"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Wizard struct {
	db      **gorm.DB // needs a double pointer to be able to update the db
	Version string
}

// New constructs an Admin API
func New(db *gorm.DB, version string) Wizard {
	wizard := Wizard{&db, version}
	return wizard
}

func (w *Wizard) IsDbNil() bool {
	return (*w.db) == nil
}

func (w *Wizard) UpdateDb(db *gorm.DB) {
	w.db = &db
}

func (w *Wizard) Landing(c *gin.Context) {
	page := c.Query("page")
	if page == "auth" {
		c.HTML(http.StatusOK, "wizard_auth.html", gin.H{
			"version": w.Version,
			"title":   "GoBlog Install Wizard",
		})
	} else {
		c.HTML(http.StatusOK, "wizard_settings.html", gin.H{
			"version": w.Version,
			"title":   "GoBlog Install Wizard",
		})
	}
}

func (w *Wizard) SaveToken(c *gin.Context) {
	clientId := c.Query("client_id")
	if clientId == "" {
		c.HTML(http.StatusOK, "wizard_auth.html", gin.H{
			"version": w.Version,
		})
		return
	}
	clientSecret := c.Query("client_secret")
	if clientSecret == "" {
		c.HTML(http.StatusOK, "wizard_auth.html", gin.H{
			"version": w.Version,
			"errors":  "Client Secret must not be empty",
		})
		return
	}
	session := sessions.Default(c)
	session.Set("client_id", clientId)
	session.Set("client_secret", clientSecret)
	err := session.Save()
	if err != nil {
		log.Println("Can't save client_id and client_secret")
		c.HTML(http.StatusOK, "wizard_auth.html", gin.H{
			"version": w.Version,
			"errors":  "Can't save client_id and client_secret: " + err.Error(),
		})
		return
	}

	c.Redirect(http.StatusFound, "https://github.com/login/oauth/authorize?client_id="+clientId)
}

type AccessTokenResponse struct {
	AccessToken string `json:"access_token"`
}

func (w *Wizard) LoginCode(c *gin.Context) error {
	code := c.Query("code")
	log.Println("LOGIN CODE: " + code)
	if w.IsDbNil() {
		return errors.New("db is nil")
	}

	session := sessions.Default(c)
	clientId, ok := session.Get("client_id").(string)
	if !ok {
		return errors.New("can't retrieve client_id")
	}

	clientSecret, ok := session.Get("client_secret").(string)
	if !ok {
		return errors.New("can't retrieve client_secret")
	}

	formData := url.Values{
		"client_id":     {clientId},
		"client_secret": {clientSecret},
		"code":          {code},
	}

	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(formData.Encode()))
	if err != nil {
		return errors.New("Error requesting access token from github: " + err.Error())
	}

	req.Header.Set("Accept", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Error requesting access token from github: " + err.Error())
	}

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("Error closing body: " + err.Error())
		}
	}(resp.Body)

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.New("Error requesting access token from github: " + err.Error())
	}

	bodyString := string(bodyBytes)
	fmt.Println("post:\n", bodyString) //todo: remove - just for debugging

	if resp.StatusCode != http.StatusOK {
		return errors.New("status was not 200: but was, " + strconv.Itoa(resp.StatusCode))
	}

	tokenResponse := &AccessTokenResponse{}
	err = json.Unmarshal(bodyBytes, &tokenResponse)
	if err != nil {
		return errors.New("Error unmarshalling token response: " + err.Error())
	}

	f, err := os.OpenFile(".env", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return errors.New("Error writing the .env file to save settings: " + err.Error())
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Println("Error closing file: " + err.Error())
		}
	}(f)
	_, err = f.WriteString("client_id=" + clientId + "\n")
	if err != nil {
		return errors.New("Error writing the .env file to save settings: " + err.Error())
	}
	_, err = f.WriteString("client_secret=" + clientSecret + "\n")
	if err != nil {
		return errors.New("Error writing the .env file to save settings: " + err.Error())
	}
	session.Delete("client_id")
	session.Delete("client_secret")
	err = session.Save()
	if err != nil {
		return errors.New("Error saving session: " + err.Error())
	}

	err = w.updateAdminUser(tokenResponse.AccessToken)
	if err != nil {
		return errors.New("Error updating admin user: " + err.Error())
	}

	return nil
}

func (w *Wizard) updateAdminUser(accessToken string) error {
	if w.IsDbNil() {
		log.Println("DB is nil in update admin user")
		return errors.New("db is nil")
	}
	_auth := auth.New(*w.db, w.Version)

	user, err := _auth.RequestUser(accessToken)
	if err != nil {
		return errors.New("couldn't get user data from github")
	}
	return w.createAdminUser(user)
}

// createAdminUser stores the GitHub user and promotes them to admin through
// the same gate as the login path, so an admin_login / admin_github_id pin in
// .env is honoured by the wizard too. A returning user is matched rather than
// duplicated.
func (w *Wizard) createAdminUser(user *auth.BlogUser) error {
	_auth := auth.New(*w.db, w.Version)
	stored, err := _auth.UpsertUser(user)
	if err != nil {
		return errors.New("Error creating user: " + err.Error())
	}
	err = _auth.EnsureAdmin(stored)
	if errors.Is(err, auth.ErrNotConfiguredAdmin) {
		return fmt.Errorf("GitHub user %q (id %s) is not the configured admin; check admin_login / admin_github_id in .env", stored.Login, stored.ProviderID)
	}
	if err != nil {
		return errors.New("Error creating admin user: " + err.Error())
	}
	return nil
}
