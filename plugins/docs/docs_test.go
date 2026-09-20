package docs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goblog/blog"
	gplugin "goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func renderCtx(t *testing.T, path, subPath string) (*gplugin.HookContext, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return &gplugin.HookContext{GinContext: c, Settings: map[string]string{"enabled": "true"}, SubPath: subPath}, w
}

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "docs" || p.DisplayName() != "Documentation" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	s := p.Settings()
	if len(s) != 1 || s[0].Key != "enabled" || s[0].DefaultValue != "false" {
		t.Errorf("settings = %+v", s)
	}
	pg := p.Pages()
	if len(pg) != 1 || pg[0].PageType != PageType || pg[0].Slug != "docs" || pg[0].Title != "Docs" || pg[0].NavOrder != 32 || !pg[0].ShowInNav {
		t.Errorf("pages = %+v", pg)
	}
	var _ gplugin.Plugin = p
}

func TestPagesManifest(t *testing.T) {
	want := []page{
		{"", "Overview", "overview.md"},
		{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
		{"plugin-api", "Plugin API reference", "plugin-api.md"},
		{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
		{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
		{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
		{"directory-formats", "Directory formats", "directory-formats.md"},
	}
	if len(pages) != len(want) {
		t.Fatalf("pages = %+v", pages)
	}
	for i := range want {
		if pages[i] != want[i] {
			t.Errorf("page %d = %+v, want %+v", i, pages[i], want[i])
		}
	}
	p := New()
	for _, pg := range pages {
		r, ok := p.rendered(pg.Slug)
		if !ok {
			t.Fatalf("page %q not rendered", pg.Slug)
		}
		if !strings.Contains(string(r.HTML), `<h1 id="`) || !strings.Contains(string(r.HTML), ">"+pg.Title+"</h1>") {
			t.Errorf("page %q must start with an H1 equal to its title; got:\n%.200s", pg.Slug, r.HTML)
		}
	}
}

func TestOnInit_CreatesPage(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	var rows []blog.Page
	db.Where("page_type = ?", PageType).Find(&rows)
	if len(rows) != 1 || rows[0].Slug != "docs" || rows[0].Title != "Docs" || !rows[0].Enabled {
		t.Errorf("rows = %+v", rows)
	}
}

func TestRenderPage(t *testing.T) {
	p := New()
	ctx, _ := renderCtx(t, "/docs", "")
	if tmpl, _ := p.RenderPage(ctx, "other"); tmpl != "" {
		t.Error("other page types are declined")
	}
	tmpl, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if tmpl != "page_content.html" || data["has_plugin_content"] != true || data["title"] != "Overview" {
		t.Fatalf("index: %q %v", tmpl, data)
	}
	for _, want := range []string{`<h1 id="overview">Overview</h1>`, `href="/docs/plugin-api"`, `href="/docs/directory-formats"`, `aria-current="page"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index missing %q", want)
		}
	}
	ctx, _ = renderCtx(t, "/docs/plugin-api", "plugin-api")
	_, data = p.RenderPage(ctx, PageType)
	html, _ = data["plugin_content"].(string)
	if data["title"] != "Plugin API reference" || !strings.Contains(html, `<h1 id="plugin-api-reference">`) {
		t.Errorf("plugin-api page: title=%v", data["title"])
	}
	// Sidebar follows the page slug, and marks the current page.
	ctx, _ = renderCtx(t, "/manual/plugin-api", "plugin-api")
	_, data = p.RenderPage(ctx, PageType)
	html, _ = data["plugin_content"].(string)
	if !strings.Contains(html, `href="/manual/writing-a-theme"`) || !strings.Contains(html, `<a class="nav-link active" aria-current="page" href="/manual/plugin-api">`) {
		t.Errorf("sidebar:\n%s", html)
	}
	for _, sp := range []string{"nope", "plugin-api/", "../x", "overview.md"} {
		ctx, _ := renderCtx(t, "/docs/"+sp, sp)
		if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" {
			t.Errorf("%q should be declined", sp)
		}
	}
}

// TestTOC_OnlyForLongerPages: a page gets a table of contents iff it has
// more than three H2/H3 headings. The reference page is the long one; every
// real page is checked against the iff; and because every shipped page now
// has more than three headings, the no-TOC side is exercised with a
// synthetic three-heading page rather than left to chance.
func TestTOC_OnlyForLongerPages(t *testing.T) {
	p := New()
	long, _ := p.rendered("plugin-api")
	if len(long.TOC) <= 3 {
		t.Fatalf("plugin-api has %d headings; the test needs more than three", len(long.TOC))
	}
	out := sidebarAndArticle("/docs", "plugin-api", long)
	if !strings.Contains(out, `class="docs-toc `) || !strings.Contains(out, `href="#exports"`) {
		t.Error("plugin-api should have a table of contents with an #exports entry")
	}
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		has := strings.Contains(sidebarAndArticle("/docs", pg.Slug, r), `class="docs-toc `)
		if has != (len(r.TOC) > 3) {
			t.Errorf("%s: %d headings, toc=%v", pg.File, len(r.TOC), has)
		}
	}
	html, toc, err := Render([]byte("# Short\n\n## One\n\n## Two\n\n### Three\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(toc) != 3 {
		t.Fatalf("synthetic page has %d headings, want 3", len(toc))
	}
	short := renderedPage{page: page{Slug: "short", Title: "Short", File: "short.md"}, HTML: html, TOC: toc}
	if strings.Contains(sidebarAndArticle("/docs", "short", short), `class="docs-toc `) {
		t.Error("a page with three headings should have no table of contents")
	}
}
