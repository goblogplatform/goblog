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

func (p *Plugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled",
			Description: "Set to 'true' to publish the plugin directory at /plugins"},
		{Key: "index_url", Type: "text", DefaultValue: defaultIndexURL, Label: "Index URL",
			Description: "index.json published by the plugin registry"},
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

// OnInit ensures the directory page exists in the pages table, the same way
// the scholar plugin creates its research page. The admin can rename or
// reorder it afterwards.
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
			if time.Since(p.fetcher.FetchedAt()) < refreshInterval(settings) {
				return nil
			}
			return p.fetcher.Refresh(indexURL(settings))
		},
	}}
}

// RenderPage is completed in the next task.
func (p *Plugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
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
