// Package directory is the plugin directory: the registry behind
// goblog.live/plugins. Repositories are submitted at /plugins/submit,
// validated and built here, approved in Admin → Plugins, and served as the
// listing, per-plugin pages and the machine-readable /plugins/index.json
// that every goblog's installer reads. It is compiled in and disabled by
// default; any goblog can host a directory by enabling it.
package directory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goblog/blog"
	gplugin "goblog/plugin"
	"goblog/plugins/directory/registry"
	"goblog/theme"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// fsThemeValidator checks a theme's files against this goblog's shared and
// default templates — the same rule the installer applies.
type fsThemeValidator struct{}

func (fsThemeValidator) Validate(_ context.Context, files map[string][]byte) error {
	return theme.ValidateFiles(files)
}

// PageType is the page type this plugin owns.
const PageType = "plugin-directory"

// ThemePageType is the page type of /themes.
const ThemePageType = "theme-directory"

// kindOf maps a page type to the directory kind it lists.
func kindOf(pageType string) string {
	switch pageType {
	case PageType:
		return KindPlugin
	case ThemePageType:
		return KindTheme
	}
	return ""
}

const (
	defaultRefresh = 360 * time.Minute
	minRefresh     = 15 * time.Minute
)

// Plugin is the directory plugin. Its Service exists once OnInit has run.
type Plugin struct {
	gplugin.BasePlugin
	userAgent      string
	db             *gorm.DB
	svc            *Service
	validator      registry.Validator
	themeValidator registry.ThemeValidator
	newSource      func(token string) registry.Source // nil → GitHub; tests inject a fake
}

// New creates the directory plugin.
func New() *Plugin {
	return &Plugin{userAgent: "goblog-directory", validator: registry.WasmValidator{}, themeValidator: fsThemeValidator{}}
}

func (p *Plugin) Name() string        { return "directory" }
func (p *Plugin) DisplayName() string { return "Plugin Directory" }
func (p *Plugin) Version() string     { return "2.0.0" }

// SetUserAgent sets the User-Agent sent to GitHub.
func (p *Plugin) SetUserAgent(ua string) { p.userAgent = ua }

// SetSource replaces the GitHub client factory (tests, or a mirror).
func (p *Plugin) SetSource(f func(token string) registry.Source) { p.newSource = f }

// SetValidator replaces the module validator (tests).
func (p *Plugin) SetValidator(v registry.Validator) { p.validator = v }

// SetThemeValidator replaces the theme validator (tests).
func (p *Plugin) SetThemeValidator(v registry.ThemeValidator) { p.themeValidator = v }

// Service is the registry behind the pages; nil until OnInit has run.
func (p *Plugin) Service() *Service { return p.svc }

func (p *Plugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled",
			Description: "Set to 'true' to host a plugin directory at /plugins"},
		{Key: "refresh_minutes", Type: "text", DefaultValue: "360", Label: "Refresh interval (minutes)",
			Description: "How often listed plugins are checked for new releases and stars (minimum 15)"},
		{Key: "github_token", Type: "password", DefaultValue: "", Label: "GitHub token",
			Description: "Optional. A token with no scopes raises the GitHub API limit from 60 to 5000 requests per hour; without one the directory still works but refreshes slowly."},
	}
}

func (p *Plugin) Pages() []gplugin.PageDefinition {
	return []gplugin.PageDefinition{
		{
			PageType:    PageType,
			Title:       "Plugins",
			Slug:        "plugins",
			ShowInNav:   true,
			NavOrder:    30,
			Description: "Browsable directory of goblog plugins",
		},
		{
			PageType:    ThemePageType,
			Title:       "Themes",
			Slug:        "themes",
			ShowInNav:   true,
			NavOrder:    31,
			Description: "Browsable directory of goblog themes",
		},
	}
}

// source returns the GitHub client for one operation, with the token the
// admin configured (if any).
func (p *Plugin) source(token string) registry.Source {
	if p.newSource != nil {
		return p.newSource(token)
	}
	g := registry.NewGitHubSource(token, "")
	g.SetUserAgent(p.userAgent)
	return g
}

// OnInit migrates the registry tables, creates the Service and ensures the
// directory page exists. The registry's ensurePages already creates the
// page for every plugin before OnInit runs, so this normally finds the row
// in place and is kept as a fallback. The admin can rename or reorder it.
//
// blog.Page.Slug has a unique index, so if some other page already uses the
// "plugins" slug (a different page type), creating our page would fail; a
// post type on that slug would be shadowed by our page. Neither must abort
// plugin.Registry.Init, so we log a warning and leave it to the operator
// instead of returning an error.
func (p *Plugin) OnInit(db *gorm.DB) error {
	if err := Migrate(db); err != nil {
		return fmt.Errorf("directory plugin: migrate: %w", err)
	}
	p.db = db
	if p.svc == nil {
		p.svc = NewService(db, p.source, p.validator, p.themeValidator, func() string { return siteURL(db) })
	}

	for _, def := range p.Pages() {
		if err := ensurePage(db, def); err != nil {
			return err
		}
	}
	return nil
}

// ensurePage creates def's page row if it does not already exist. A slug
// collision with some other page type or a post type must not abort
// plugin.Registry.Init, so it is logged and left to the operator instead of
// returned as an error.
func ensurePage(db *gorm.DB, def gplugin.PageDefinition) error {
	var page blog.Page
	err := db.Where("page_type = ?", def.PageType).First(&page).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query page: %w", err)
	}
	var existing blog.Page
	if err := db.Where("slug = ?", def.Slug).First(&existing).Error; err == nil {
		log.Printf("Directory plugin: page slug %q is already used by a %q page; rename it and restart to create the %s page", def.Slug, existing.PageType, def.Title)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query slug: %w", err)
	}
	var postType blog.PostType
	if err := db.Where("slug = ?", def.Slug).First(&postType).Error; err == nil {
		log.Printf("Directory plugin: page slug %q is already used by the %q post type; rename it and restart to create the %s page", def.Slug, postType.Name, def.Title)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query post type slug: %w", err)
	}
	page = blog.Page{
		Title:     def.Title,
		Slug:      def.Slug,
		PageType:  def.PageType,
		ShowInNav: def.ShowInNav,
		NavOrder:  def.NavOrder,
		Enabled:   true,
	}
	if err := db.Create(&page).Error; err != nil {
		return fmt.Errorf("directory plugin: create page: %w", err)
	}
	log.Printf("Directory plugin: created %s page", def.Title)
	return nil
}

// siteURL is the configured site_url setting ("" when unset); detail_url
// in the index is built from it.
func siteURL(db *gorm.DB) string {
	var s blog.Setting
	if err := db.Where("key = ?", "site_url").First(&s).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(s.Value)
}

// settings reads this plugin's settings outside a hook (the admin API
// needs the token and the enabled flag).
func (p *Plugin) settings() map[string]string {
	out := map[string]string{}
	if p.db == nil {
		return out
	}
	var rows []gplugin.PluginSetting
	p.db.Where("plugin_name = ?", p.Name()).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out
}

// Token is the configured github_token ("" for anonymous access).
func (p *Plugin) Token() string { return p.settings()["github_token"] }

// Hosted reports whether this site runs a directory: initialised and enabled.
func (p *Plugin) Hosted() bool { return p.svc != nil && p.settings()["enabled"] == "true" }

// ScheduledJobs ticks every minute and refreshes every listed plugin once
// the last refresh is older than refresh_minutes, so the setting takes
// effect without a restart.
func (p *Plugin) ScheduledJobs() []gplugin.ScheduledJob {
	return []gplugin.ScheduledJob{{
		Name:     "refresh-directory",
		Interval: time.Minute,
		Run: func(_ *gorm.DB, settings map[string]string) error {
			if settings["enabled"] != "true" || p.svc == nil {
				return nil
			}
			if time.Since(p.svc.LastRefresh()) < refreshInterval(settings) {
				return nil
			}
			return p.svc.RefreshAll(context.Background(), settings["github_token"])
		},
	}}
}

const unavailableHTML = `<div class="alert alert-warning" role="alert">The plugin directory is unavailable right now. Please check back later.</div>`

// RenderPage serves the listing (""), the raw index ("index.json"), the
// submission page ("submit"), one plugin's page ("<name>") and its JSON
// ("<name>.json"). Anything else — including plugins that are not approved
// — is declined, which blog turns into a 404.
func (p *Plugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
	kind := kindOf(pageType)
	if kind == "" || p.svc == nil {
		return "", nil
	}
	c := ctx.GinContext
	base := basePath(c)

	switch {
	case ctx.SubPath == "":
		query := strings.TrimSpace(c.Query("q"))
		html, err := renderListingFor(kind, base, query, p.listing(kind, query))
		if err != nil {
			log.Printf("Directory plugin: render listing: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html}

	case ctx.SubPath == "index.json":
		raw, _ := p.svc.Index(kind)
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case ctx.SubPath == "submit":
		return p.renderSubmit(ctx, base, kind)

	case strings.HasSuffix(ctx.SubPath, ".json") && validName(strings.TrimSuffix(ctx.SubPath, ".json")):
		d, ok := p.svc.Detail(kind, strings.TrimSuffix(ctx.SubPath, ".json"))
		if !ok {
			return "", nil
		}
		raw, err := marshalJSON(d)
		if err != nil {
			log.Printf("Directory plugin: encode %s: %v", d.Name, err)
			return "", nil
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case validName(ctx.SubPath):
		d, ok := p.svc.Detail(kind, ctx.SubPath)
		if !ok {
			return "", nil
		}
		html, err := renderDetailFor(kind, base, d)
		if err != nil {
			log.Printf("Directory plugin: render %s: %v", d.Name, err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML, "title": d.DisplayName}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html, "title": d.DisplayName}
	}
	return "", nil
}

// refreshInterval reads refresh_minutes: unparseable or non-positive falls
// back to the default; anything below the minimum is raised to it, because
// every refresh spends GitHub API quota on every listed plugin.
func refreshInterval(settings map[string]string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(settings["refresh_minutes"]))
	if err != nil || n <= 0 {
		return defaultRefresh
	}
	if d := time.Duration(n) * time.Minute; d >= minRefresh {
		return d
	}
	return minRefresh
}
