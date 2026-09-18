package admin

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"goblog/plugin/installer"

	"github.com/gin-gonic/gin"
)

// requireInstaller checks admin auth and that the installer is wired. It
// writes the response and returns nil when the caller should stop.
func (a *Admin) requireInstaller(c *gin.Context) *installer.Installer {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return nil
	}
	if a.Installer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "plugin installer is not available"})
		return nil
	}
	return a.Installer
}

// installerStatus maps installer errors to HTTP status codes.
func installerStatus(err error) int {
	switch {
	case errors.Is(err, installer.ErrNotFound), errors.Is(err, installer.ErrNotInstalled):
		return http.StatusNotFound
	case errors.Is(err, installer.ErrAlreadyInstalled):
		return http.StatusConflict
	case errors.Is(err, installer.ErrDynamicDisabled), errors.Is(err, installer.ErrWasmDisabled):
		return http.StatusPreconditionFailed
	case errors.Is(err, installer.ErrIncompatible), errors.Is(err, installer.ErrNotDynamic),
		errors.Is(err, installer.ErrChecksum), errors.Is(err, installer.ErrLoad):
		return http.StatusUnprocessableEntity
	case errors.Is(err, installer.ErrDirectoryUnavailable):
		return http.StatusBadGateway
	case errors.Is(err, installer.ErrUpToDate):
		return http.StatusBadRequest
	case errors.Is(err, installer.ErrDownload):
		return http.StatusBadGateway
	case errors.Is(err, installer.ErrWrite):
		return http.StatusInsufficientStorage
	}
	return 0
}

func writeInstallerError(c *gin.Context, err error) {
	if code := installerStatus(err); code != 0 {
		c.JSON(code, gin.H{"message": err.Error()})
		return
	}
	log.Printf("plugin installer: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "plugin operation failed; see the server log"})
}

// PluginStatus returns installed plugins and the directory entries not yet installed.
func (a *Admin) PluginStatus(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	c.JSON(http.StatusOK, inst.Status())
}

// PluginDirectory returns available plugins filtered by ?q= and ordered by ?sort= (stars|name|newest).
func (a *Admin) PluginDirectory(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	avail := inst.Status().Available // already sorted by stars, name
	if q := strings.ToLower(strings.TrimSpace(c.Query("q"))); q != "" {
		var filtered []installer.Available
		for _, p := range avail {
			hay := strings.ToLower(p.Name + " " + p.DisplayName + " " + p.Description + " " + p.Author)
			if strings.Contains(hay, q) {
				filtered = append(filtered, p)
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
		avail = []installer.Available{}
	}
	c.JSON(http.StatusOK, avail)
}

type pluginRequest struct {
	Name string `json:"name"`
}

func bindPluginName(c *gin.Context) (string, bool) {
	var req pluginRequest
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "plugin name is required"})
		return "", false
	}
	return strings.TrimSpace(req.Name), true
}

// InstallPlugin installs a directory plugin: POST {name}.
func (a *Admin) InstallPlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	name, ok := bindPluginName(c)
	if !ok {
		return
	}
	res, err := inst.Install(c.Request.Context(), name)
	if err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// UpdatePlugin updates an installed dynamic plugin to the directory version: POST {name}.
func (a *Admin) UpdatePlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	name, ok := bindPluginName(c)
	if !ok {
		return
	}
	res, err := inst.Update(c.Request.Context(), name)
	if err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// UninstallPlugin removes a dynamic plugin: DELETE /api/v1/plugins/:name.
func (a *Admin) UninstallPlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	if err := inst.Uninstall(c.Param("name")); err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Uninstalled " + c.Param("name")})
}

// RefreshPluginDirectory re-fetches the directory index now.
func (a *Admin) RefreshPluginDirectory(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	if err := inst.Refresh(); err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Directory refreshed"})
}

// AdminPlugins renders the Plugins admin page; the data is loaded by the page over the API.
func (a *Admin) AdminPlugins(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin_plugins.html", gin.H{
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}
