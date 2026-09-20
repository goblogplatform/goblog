// Package directory provides the plugin directory: a browsable list of goblog
// plugins at /plugins, per-plugin pages at /plugins/<name>, and the
// machine-readable index at /plugins/index.json. The content comes from an
// index built by the registry repository (github.com/goblogplatform/plugins)
// and is fetched into memory; this plugin only renders it. It is what runs
// goblog.live/plugins and is disabled by default everywhere else.
package directory

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"goblog/blog"
	gplugin "goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PageType is the page type this plugin owns.
const PageType = "plugin-directory"

const (
	defaultIndexURL = "https://goblogplatform.github.io/plugins/index.json"
	defaultRefresh  = 15 * time.Minute
)

// Plugin renders the plugin directory from a remote index.
type Plugin struct {
	gplugin.BasePlugin
	fetcher *Fetcher
}

// New creates the directory plugin.
func New() *Plugin {
	return &Plugin{fetcher: NewFetcher(nil)}
}

func (p *Plugin) Name() string        { return "directory" }
func (p *Plugin) DisplayName() string { return "Plugin Directory" }
func (p *Plugin) Version() string     { return "1.0.0" }

// SetUserAgent sets the User-Agent sent to the registry.
func (p *Plugin) SetUserAgent(ua string) { p.fetcher.SetUserAgent(ua) }

func (p *Plugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled",
			Description: "Set to 'true' to publish the plugin directory at /plugins"},
		{Key: "index_url", Type: "text", DefaultValue: defaultIndexURL, Label: "Index URL",
			Description: "index.json published by the plugin registry. Only point this at a registry you trust: its README, changelog and release-note HTML is shown as-is."},
		{Key: "refresh_minutes", Type: "text", DefaultValue: "15", Label: "Refresh interval (minutes)",
			Description: "How often the index is re-fetched"},
	}
}

func (p *Plugin) Pages() []gplugin.PageDefinition {
	return []gplugin.PageDefinition{{
		PageType:    PageType,
		Title:       "Plugins",
		Slug:        "plugins",
		ShowInNav:   true,
		NavOrder:    30,
		Description: "Browsable directory of goblog plugins",
	}}
}

// OnInit ensures the directory page exists in the pages table. The registry's
// ensurePages already creates it for every plugin before OnInit runs, so this
// normally finds the row in place and is kept as a fallback. The admin can
// rename or reorder the page afterwards.
//
// blog.Page.Slug has a unique index, so if some other page already uses the
// "plugins" slug (a different page type), creating our page would fail. That
// must not abort plugin.Registry.Init, which stops at the first error and
// would skip settings seeding and OnInit for every plugin registered after
// this one. So we log a warning and leave initialization to the operator
// instead of returning an error.
func (p *Plugin) OnInit(db *gorm.DB) error {
	var page blog.Page
	err := db.Where("page_type = ?", PageType).First(&page).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query page: %w", err)
	}
	def := p.Pages()[0]
	var existing blog.Page
	if err := db.Where("slug = ?", def.Slug).First(&existing).Error; err == nil {
		log.Printf("Directory plugin: page slug %q is already used by a %q page; rename it and restart to create the plugin directory page", def.Slug, existing.PageType)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query slug: %w", err)
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
	log.Println("Directory plugin: created plugins page")
	return nil
}

// ScheduledJobs ticks every minute and refreshes the index once it is older
// than refresh_minutes, so the setting takes effect without a restart.
func (p *Plugin) ScheduledJobs() []gplugin.ScheduledJob {
	return []gplugin.ScheduledJob{{
		Name:     "refresh-index",
		Interval: time.Minute,
		Run: func(_ *gorm.DB, settings map[string]string) error {
			if settings["enabled"] != "true" {
				return nil
			}
			if time.Since(p.fetcher.FetchedAt()) < refreshInterval(settings) {
				return nil
			}
			return p.fetcher.Refresh(indexURL(settings))
		},
	}}
}

const unavailableHTML = `<div class="alert alert-warning" role="alert">The plugin directory is unavailable right now. Please check back later.</div>`

// RenderPage serves the listing (""), the raw index ("index.json") and one
// plugin's page ("<name>"). Anything else is declined, which blog turns into
// a 404. Errors from the registry are logged for the operator and shown to
// readers only as "unavailable".
func (p *Plugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
	if pageType != PageType {
		return "", nil
	}
	c := ctx.GinContext
	base := basePath(c)

	switch {
	case ctx.SubPath == "":
		p.fetcher.Ensure(indexURL(ctx.Settings))
		_, entries, ok := p.fetcher.Index()
		if !ok {
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		sorted := append([]Entry(nil), entries...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Stars != sorted[j].Stars {
				return sorted[i].Stars > sorted[j].Stars
			}
			return sorted[i].Name < sorted[j].Name
		})
		html, err := renderListing(base, sorted)
		if err != nil {
			log.Printf("Directory plugin: render listing: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html}

	case ctx.SubPath == "index.json":
		p.fetcher.Ensure(indexURL(ctx.Settings))
		raw, _, ok := p.fetcher.Index()
		if !ok {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin directory index unavailable"})
			return "", nil
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case validName(ctx.SubPath):
		p.fetcher.Ensure(indexURL(ctx.Settings))
		e, ok := p.fetcher.Entry(ctx.SubPath)
		if !ok {
			return "", nil
		}
		notice := ""
		d, err := p.fetcher.Detail(e.Name)
		if err != nil {
			log.Printf("Directory plugin: %v", err)
			notice = "Details for this plugin are unavailable right now. Please check back later."
		}
		html, err := renderDetail(base, e, d, notice)
		if err != nil {
			log.Printf("Directory plugin: render %s: %v", e.Name, err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML, "title": e.DisplayName}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html, "title": e.DisplayName}
	}
	return "", nil
}

func indexURL(settings map[string]string) string {
	if u := strings.TrimSpace(settings["index_url"]); u != "" {
		return u
	}
	return defaultIndexURL
}

func refreshInterval(settings map[string]string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(settings["refresh_minutes"]))
	if err != nil || n <= 0 {
		return defaultRefresh
	}
	return time.Duration(n) * time.Minute
}
