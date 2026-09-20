package docs

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"strings"

	"goblog/blog"
	gplugin "goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PageType is the page type this plugin owns.
const PageType = "docs"

//go:embed content/*.md
var contentFS embed.FS

//go:embed templates/page.html
var pageTemplateSrc string

var pageTemplate = template.Must(template.New("page").Parse(pageTemplateSrc))

// renderedPage is a content file rendered once at construction.
type renderedPage struct {
	page
	HTML template.HTML
	TOC  []Heading
}

// Plugin serves the builder documentation at /docs.
type Plugin struct {
	gplugin.BasePlugin
	bySlug map[string]renderedPage
}

// New renders every page. A page that fails to render is a build defect,
// not a runtime condition, so it panics — the package tests catch it first.
func New() *Plugin {
	p := &Plugin{bySlug: make(map[string]renderedPage, len(pages))}
	for _, pg := range pages {
		src, err := contentFS.ReadFile("content/" + pg.File)
		if err != nil {
			panic(fmt.Sprintf("docs: %s: %v", pg.File, err))
		}
		html, toc, err := Render(src)
		if err != nil {
			panic(fmt.Sprintf("docs: render %s: %v", pg.File, err))
		}
		p.bySlug[pg.Slug] = renderedPage{page: pg, HTML: html, TOC: toc}
	}
	return p
}

func (p *Plugin) Name() string        { return "docs" }
func (p *Plugin) DisplayName() string { return "Documentation" }
func (p *Plugin) Version() string     { return "1.0.0" }

func (p *Plugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled",
			Description: "Set to 'true' to publish the plugin and theme documentation at /docs"},
	}
}

func (p *Plugin) Pages() []gplugin.PageDefinition {
	return []gplugin.PageDefinition{{
		PageType:    PageType,
		Title:       "Docs",
		Slug:        "docs",
		ShowInNav:   true,
		NavOrder:    32,
		Description: "How to build and publish goblog plugins and themes",
	}}
}

// OnInit ensures the page row exists (the registry normally creates it
// first; this is the fallback). A foreign page or a post type on the "docs"
// slug is left alone with a log line, as the directory plugin does, so Init
// continues.
func (p *Plugin) OnInit(db *gorm.DB) error {
	var existing blog.Page
	err := db.Where("page_type = ?", PageType).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("docs plugin: query page: %w", err)
	}
	def := p.Pages()[0]
	if err := db.Where("slug = ?", def.Slug).First(&existing).Error; err == nil {
		log.Printf("Docs plugin: page slug %q is already used by a %q page; rename it and restart to create the docs page", def.Slug, existing.PageType)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("docs plugin: query slug: %w", err)
	}
	var postType blog.PostType
	if err := db.Where("slug = ?", def.Slug).First(&postType).Error; err == nil {
		log.Printf("Docs plugin: page slug %q is already used by the %q post type; rename it and restart to create the docs page", def.Slug, postType.Name)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("docs plugin: query post type slug: %w", err)
	}
	row := blog.Page{Title: def.Title, Slug: def.Slug, PageType: def.PageType, ShowInNav: def.ShowInNav, NavOrder: def.NavOrder, Enabled: true}
	if err := db.Create(&row).Error; err != nil {
		return fmt.Errorf("docs plugin: create page: %w", err)
	}
	log.Println("Docs plugin: created docs page")
	return nil
}

// rendered looks a page up by slug.
func (p *Plugin) rendered(slug string) (renderedPage, bool) {
	r, ok := p.bySlug[slug]
	return r, ok
}

// RenderPage serves the index ("") and one page per slug; anything else is
// declined so blog answers 404.
func (p *Plugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
	if pageType != PageType {
		return "", nil
	}
	r, ok := p.rendered(ctx.SubPath)
	if !ok {
		return "", nil
	}
	base := basePath(ctx.GinContext)
	return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": sidebarAndArticle(base, ctx.SubPath, r), "title": r.Title}
}

// basePath is the page's URL prefix ("/docs"), from the request so links
// follow a renamed slug.
func basePath(c *gin.Context) string {
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	slug, _, _ := strings.Cut(path, "/")
	return "/" + slug
}

// sidebarAndArticle lays out the sidebar (every page, current one marked),
// the table of contents when the page is long enough to need one, and the
// article.
func sidebarAndArticle(base, current string, r renderedPage) string {
	type item struct {
		Href, Title string
		Current     bool
	}
	items := make([]item, 0, len(pages))
	for _, pg := range pages {
		href := base
		if pg.Slug != "" {
			href += "/" + pg.Slug
		}
		items = append(items, item{Href: href, Title: pg.Title, Current: pg.Slug == current})
	}
	var buf bytes.Buffer
	err := pageTemplate.Execute(&buf, map[string]any{
		"Items":  items,
		"TOC":    r.TOC,
		"HasTOC": len(r.TOC) > 3,
		"HTML":   r.HTML,
	})
	if err != nil {
		log.Printf("Docs plugin: render layout: %v", err)
		return string(r.HTML)
	}
	return buf.String()
}
