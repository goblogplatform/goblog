package admin

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"goblog/theme"
	tinstaller "goblog/theme/installer"

	"github.com/gin-gonic/gin"
)

// requireThemes checks admin auth and that the theme installer is wired. It
// writes the response and returns nil when the caller should stop.
func (a *Admin) requireThemes(c *gin.Context) *tinstaller.Installer {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return nil
	}
	if a.Themes == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "theme installer is not available"})
		return nil
	}
	return a.Themes
}

// themeStatus maps theme installer errors to HTTP status codes.
func themeStatus(err error) int {
	switch {
	case errors.Is(err, tinstaller.ErrNotFound), errors.Is(err, tinstaller.ErrNotInstalled):
		return http.StatusNotFound
	case errors.Is(err, tinstaller.ErrAlreadyInstalled), errors.Is(err, tinstaller.ErrActive), errors.Is(err, tinstaller.ErrUpToDate):
		return http.StatusConflict
	case errors.Is(err, tinstaller.ErrBuiltin), errors.Is(err, tinstaller.ErrIncompatible), errors.Is(err, tinstaller.ErrNotTheme),
		errors.Is(err, tinstaller.ErrChecksum), errors.Is(err, tinstaller.ErrLoad):
		return http.StatusUnprocessableEntity
	// Upstream (the directory or the download) and disk failures get the
	// same codes as the plugin installer: 502 says "not us", 507 says "fix
	// the directory permissions", so the page can phrase them accordingly.
	case errors.Is(err, tinstaller.ErrDirectoryUnavailable), errors.Is(err, tinstaller.ErrDownload):
		return http.StatusBadGateway
	case errors.Is(err, tinstaller.ErrWrite):
		return http.StatusInsufficientStorage
	}
	return 0
}

func writeThemeError(c *gin.Context, err error) {
	if code := themeStatus(err); code != 0 {
		c.JSON(code, gin.H{"message": err.Error()})
		return
	}
	log.Printf("theme installer: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "theme operation failed; see the server log"})
}

// ThemeStatus returns installed themes and the directory entries not yet installed.
func (a *Admin) ThemeStatus(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	c.JSON(http.StatusOK, inst.Status())
}

// ThemeDirectory returns available themes filtered by ?q= and ordered by ?sort= (stars|name|newest).
func (a *Admin) ThemeDirectory(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	avail := inst.Status().Available // already sorted by stars, name
	if q := strings.ToLower(strings.TrimSpace(c.Query("q"))); q != "" {
		var filtered []tinstaller.Available
		for _, t := range avail {
			hay := strings.ToLower(t.Name + " " + t.DisplayName + " " + t.Description + " " + t.Author)
			if strings.Contains(hay, q) {
				filtered = append(filtered, t)
			}
		}
		avail = filtered
	}
	switch c.Query("sort") {
	case "name":
		sort.SliceStable(avail, func(i, j int) bool { return avail[i].Name < avail[j].Name })
	case "newest":
		sort.SliceStable(avail, func(i, j int) bool { return avail[i].ReleasedAt > avail[j].ReleasedAt })
	}
	if avail == nil {
		avail = []tinstaller.Available{}
	}
	c.JSON(http.StatusOK, avail)
}

type themeRequest struct {
	Name string `json:"name"`
}

func bindThemeName(c *gin.Context) (string, bool) {
	var req themeRequest
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "theme name is required"})
		return "", false
	}
	return strings.TrimSpace(req.Name), true
}

// InstallTheme installs a directory theme: POST {name}.
func (a *Admin) InstallTheme(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	name, ok := bindThemeName(c)
	if !ok {
		return
	}
	res, err := inst.Install(c.Request.Context(), name)
	if err != nil {
		writeThemeError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// UpdateTheme updates an installed theme to the directory version: POST {name}.
func (a *Admin) UpdateTheme(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	name, ok := bindThemeName(c)
	if !ok {
		return
	}
	res, err := inst.Update(c.Request.Context(), name)
	if err != nil {
		writeThemeError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// ActivateTheme makes a built-in or installed theme the site's theme: POST {name}.
func (a *Admin) ActivateTheme(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	name, ok := bindThemeName(c)
	if !ok {
		return
	}
	if err := inst.ActivateTheme(name); err != nil {
		writeThemeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Activated " + name})
}

// UninstallTheme removes an installed theme: DELETE /api/v1/themes/:name.
func (a *Admin) UninstallTheme(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	if err := inst.Uninstall(c.Param("name")); err != nil {
		writeThemeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Uninstalled " + c.Param("name")})
}

// RefreshThemeDirectory re-fetches the theme directory index now.
func (a *Admin) RefreshThemeDirectory(c *gin.Context) {
	inst := a.requireThemes(c)
	if inst == nil {
		return
	}
	if err := inst.Refresh(); err != nil {
		writeThemeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Directory refreshed"})
}

// ThemeScreenshot serves an on-disk theme's own screenshot for the Themes
// page cards: GET /admin/themes/:name/screenshot. theme.Screenshot only
// resolves a name theme.ValidName accepts (letters, digits, "_", "-") that
// is a theme on disk with a screenshot, so nothing else in the URL ever
// reaches the filesystem as a path. Admin-only like the rest of the page;
// the file is static, so the browser may cache it for the session.
func (a *Admin) ThemeScreenshot(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	name := c.Param("name")
	path, ok := theme.Screenshot(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"message": "no screenshot for theme " + name})
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.File(path)
}

// AdminThemes renders the Themes admin page; the data is loaded by the page over the API.
func (a *Admin) AdminThemes(c *gin.Context) {
	if !a.b.RequireAdminPage(c) {
		return
	}
	c.HTML(http.StatusOK, "admin_themes.html", gin.H{
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}
