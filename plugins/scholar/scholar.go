// Package scholar provides a Google Scholar integration plugin for goblog.
// It displays academic publications on a dynamic "research" page, with
// caching and throttle resilience via the compscidr/scholar library.
package scholar

import (
	"errors"
	"fmt"
	"goblog/blog"
	"html"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gplugin "goblog/plugin"

	scholarlib "github.com/compscidr/scholar"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ScholarPlugin displays Google Scholar publications.
type ScholarPlugin struct {
	gplugin.BasePlugin
	sch         *scholarlib.Scholar
	scholarOnce sync.Once
}

// New creates a new scholar plugin.
func New() *ScholarPlugin {
	return &ScholarPlugin{}
}

func (p *ScholarPlugin) Name() string        { return "scholar" }
func (p *ScholarPlugin) DisplayName() string { return "Google Scholar" }
func (p *ScholarPlugin) Version() string     { return "1.0.0" }

func (p *ScholarPlugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled", Description: "Set to 'true' to enable the research page"},
		{Key: "scholar_id", Type: "text", DefaultValue: "", Label: "Google Scholar ID", Description: "Your Google Scholar profile ID (e.g. SbUmSEAAAAAJ)"},
		{Key: "article_limit", Type: "text", DefaultValue: "50", Label: "Article Limit", Description: "Maximum number of articles to display"},
		{Key: "profile_cache", Type: "text", DefaultValue: "profiles.json", Label: "Profile Cache File", Description: "File path for profile cache"},
		{Key: "article_cache", Type: "text", DefaultValue: "articles.json", Label: "Article Cache File", Description: "File path for article cache"},
	}
}

func (p *ScholarPlugin) OnInit(db *gorm.DB) error {
	// Ensure a research page exists in the pages table.
	// The user can customize title, slug, hero, nav order via admin.
	var page blog.Page
	result := db.Where("page_type = ?", "research").First(&page)
	if result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("scholar plugin: failed to query research page: %w", result.Error)
		}
		// No research page exists — create the default
		page = blog.Page{
			Title:    "Research",
			Slug:     "research",
			PageType: "research",
			ShowInNav: true,
			NavOrder: 20,
			Enabled:  true,
		}
		if err := db.Create(&page).Error; err != nil {
			return fmt.Errorf("scholar plugin: failed to create research page: %w", err)
		}
		log.Println("Scholar plugin: created research page")
	}

	// Migrate ScholarID from page record to plugin settings (backward compat)
	if page.ScholarID != "" {
		var existing gplugin.PluginSetting
		if err := db.Where("plugin_name = ? AND key = ?", "scholar", "scholar_id").First(&existing).Error; err != nil || existing.Value == "" {
			db.Where("plugin_name = ? AND key = ?", "scholar", "scholar_id").
				Assign(gplugin.PluginSetting{Value: page.ScholarID}).
				FirstOrCreate(&gplugin.PluginSetting{PluginName: "scholar", Key: "scholar_id", Value: page.ScholarID})
			log.Printf("Scholar plugin: migrated scholar_id %s from page to plugin settings", page.ScholarID)
		}
	}

	return nil
}

func (p *ScholarPlugin) Pages() []gplugin.PageDefinition {
	return []gplugin.PageDefinition{
		{
			PageType:    "research",
			Title:       "Research",
			Slug:        "research",
			ShowInNav:   true,
			NavOrder:    20,
			Description: "Displays Google Scholar publications",
		},
	}
}

func (p *ScholarPlugin) ensureScholar(settings map[string]string) {
	p.scholarOnce.Do(func() {
		profileCache := settings["profile_cache"]
		articleCache := settings["article_cache"]
		if profileCache == "" {
			profileCache = "profiles.json"
		}
		if articleCache == "" {
			articleCache = "articles.json"
		}
		p.sch = scholarlib.New(profileCache, articleCache)
	})
}

// unavailableHTML is what visitors see when publications can't be fetched.
// The underlying error goes to the server log only: it names the Google URL
// and status, which is noise for a reader and useful for the operator.
const unavailableHTML = `<div class="alert alert-warning" role="alert">Publications are temporarily unavailable. Please check back later.</div>`

func (p *ScholarPlugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
	if pageType != "research" {
		return "", nil
	}

	data := gin.H{"has_plugin_content": true}

	settings := ctx.Settings
	scholarID := settings["scholar_id"]
	if scholarID == "" {
		data["plugin_content"] = `<div class="alert alert-warning" role="alert">Google Scholar ID not configured. Set it in the Scholar plugin settings.</div>`
		return "page_content.html", data
	}

	limitStr := settings["article_limit"]
	limit := 50
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	p.ensureScholar(settings)

	articles, err := p.sch.QueryProfileWithMemoryCache(scholarID, limit)
	if err != nil {
		log.Printf("Scholar query failed: %v", err)
		data["plugin_content"] = unavailableHTML
		return "page_content.html", data
	}

	sortArticlesByDateDesc(articles)
	p.sch.SaveCache(settings["profile_cache"], settings["article_cache"])

	data["plugin_content"] = renderArticlesHTML(articles)
	return "page_content.html", data
}

// safeHref returns the URL only if it uses http or https scheme, otherwise empty.
func safeHref(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return html.EscapeString(rawURL)
}

// renderArticlesHTML generates the HTML for the articles list.
func renderArticlesHTML(articles []*scholarlib.Article) string {
	if len(articles) == 0 {
		return `<p>No publications found.</p>`
	}

	var b strings.Builder
	for _, a := range articles {
		b.WriteString(`<div style="margin-bottom: 12px; padding-bottom: 12px; border-bottom: 1px solid #eee;">`)

		// Title — link only if URL is safe
		href := safeHref(a.ScholarURL)
		if href != "" {
			b.WriteString(`<div><a href="` + href + `">` + html.EscapeString(a.Title) + `</a></div>`)
		} else {
			b.WriteString(`<div>` + html.EscapeString(a.Title) + `</div>`)
		}

		if a.Authors != "" {
			b.WriteString(`<div style="color: #666; font-size: 13px;">` + html.EscapeString(a.Authors) + `</div>`)
		}

		// Meta line: year · journal · citations
		var meta []string
		if a.Year > 0 {
			meta = append(meta, strconv.Itoa(a.Year))
		}
		if a.Journal != "" {
			meta = append(meta, html.EscapeString(a.Journal))
		}
		if a.NumCitations > 0 {
			meta = append(meta, strconv.Itoa(a.NumCitations)+" citations")
		}
		if len(meta) > 0 {
			b.WriteString(`<div style="color: #888; font-size: 13px;">` + strings.Join(meta, " &middot; ") + `</div>`)
		}

		b.WriteString(`</div>`)
	}
	return b.String()
}

func (p *ScholarPlugin) ScheduledJobs() []gplugin.ScheduledJob {
	return []gplugin.ScheduledJob{
		{
			Name:     "scholar-cache-refresh",
			Interval: 24 * time.Hour,
			Run: func(db *gorm.DB, settings map[string]string) error {
				scholarID := settings["scholar_id"]
				if scholarID == "" || settings["enabled"] != "true" {
					return nil
				}
				p.ensureScholar(settings)
				limit := 50
				fmt.Sscanf(settings["article_limit"], "%d", &limit)
				_, err := p.sch.QueryProfileWithMemoryCache(scholarID, limit)
				if err == nil {
					p.sch.SaveCache(settings["profile_cache"], settings["article_cache"])
				}
				return err
			},
		},
	}
}

func sortArticlesByDateDesc(articles []*scholarlib.Article) {
	sort.Slice(articles, func(i, j int) bool {
		if articles[i].Year != articles[j].Year {
			return articles[i].Year > articles[j].Year
		}
		if articles[i].Month != articles[j].Month {
			return articles[i].Month > articles[j].Month
		}
		return articles[i].Day > articles[j].Day
	})
}
