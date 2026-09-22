package docs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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
		// Each file opens with "# <title>" — the theme shows that title as
		// the page heading, so the rendered article carries no H1 of its own.
		src, _ := contentFS.ReadFile("content/" + pg.File)
		if !strings.HasPrefix(string(src), "# "+pg.Title+"\n") {
			t.Errorf("page %q must start with an H1 equal to its title; got:\n%.80s", pg.Slug, src)
		}
		if strings.Contains(string(r.HTML), "<h1") {
			t.Errorf("page %q: the article must not repeat the title as an <h1>:\n%.200s", pg.Slug, r.HTML)
		}
	}
}

func TestOnInit_CreatesPage(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{}, &blog.PostType{})
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

// TestOnInit_ForeignSlug: a page of another type or a post type already at
// "docs" is left alone with a log line; OnInit neither fails nor creates a
// docs page.
func TestOnInit_ForeignSlug(t *testing.T) {
	for name, seed := range map[string]any{
		"page":      &blog.Page{Title: "Mine", Slug: "docs", PageType: blog.PageTypeCustom, Enabled: true},
		"post type": &blog.PostType{Name: "Docs", Slug: "docs"},
	} {
		db, _ := gorm.Open(sqlite.Open(":memory:"))
		db.AutoMigrate(&blog.Page{}, &blog.PostType{})
		if err := db.Create(seed).Error; err != nil {
			t.Fatal(err)
		}
		if err := New().OnInit(db); err != nil {
			t.Fatalf("%s: OnInit must not fail when the slug is taken, got %v", name, err)
		}
		var count int64
		db.Model(&blog.Page{}).Where("page_type = ?", PageType).Count(&count)
		if count != 0 {
			t.Errorf("%s: expected no docs page, got %d", name, count)
		}
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
	for _, want := range []string{`href="/docs/plugin-api"`, `href="/docs/directory-formats"`, `aria-current="page"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index missing %q", want)
		}
	}
	ctx, _ = renderCtx(t, "/docs/plugin-api", "plugin-api")
	_, data = p.RenderPage(ctx, PageType)
	html, _ = data["plugin_content"].(string)
	if data["title"] != "Plugin API reference" || data["page_title"] != "Plugin API reference" || !strings.Contains(html, `<h2 id="exports">`) {
		t.Errorf("plugin-api page: title=%v page_title=%v", data["title"], data["page_title"])
	}
	// Sidebar and in-article links follow the page slug, and the sidebar
	// marks the current page. publishing-a-plugin.md links /docs#the-directory;
	// the sidebar never links the index with an anchor, so that link proves
	// the article was rewritten too.
	ctx, _ = renderCtx(t, "/manual/publishing-a-plugin", "publishing-a-plugin")
	_, data = p.RenderPage(ctx, PageType)
	html, _ = data["plugin_content"].(string)
	if !strings.Contains(html, `href="/manual/writing-a-theme"`) || !strings.Contains(html, `<a class="nav-link active" aria-current="page" href="/manual/publishing-a-plugin">`) {
		t.Errorf("sidebar:\n%s", html)
	}
	if !strings.Contains(html, `href="/manual#the-directory"`) || strings.Contains(html, `href="/docs`) {
		t.Errorf("in-article links should follow the /manual slug; got %d href=\"/docs occurrences", strings.Count(html, `href="/docs`))
	}
	// A page whose slug still is "docs" is served untouched.
	ctx, _ = renderCtx(t, "/docs/publishing-a-plugin", "publishing-a-plugin")
	_, data = p.RenderPage(ctx, PageType)
	if html, _ := data["plugin_content"].(string); !strings.Contains(html, `href="/docs#the-directory"`) {
		t.Error("links keep the /docs prefix when the slug is docs")
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

func TestSidebarAndArticle_RewritesOnlyWholeDocsPrefixes(t *testing.T) {
	r := renderedPage{page: pages[0], HTML: `<a href="/docs">i</a> <a href="/docs/x">x</a> <a href="/docs#a">a</a> <a href="/docs-faq">n</a> <a href="https://example.com/docs/y">e</a>`}
	got := sidebarAndArticle("/manual", "", r)
	for _, want := range []string{`href="/manual">i`, `href="/manual/x"`, `href="/manual#a"`, `href="/docs-faq"`, `href="https://example.com/docs/y"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in article:\n%s", want, got)
		}
	}
}

// TestSearch: the plugin answers the site search with pages whose title or
// text matches, a snippet around the match, under the page's current slug.
func TestSearch(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{}, &blog.PostType{})
	p := New()
	var _ gplugin.Searcher = p
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	ctx, _ := renderCtx(t, "/search?q=x", "")
	ctx.DB = db

	got := p.Search(ctx, "Writing a theme")
	if len(got) == 0 || got[0].Title != "Writing a theme" || got[0].URL != "/docs/writing-a-theme" || got[0].Kind != "Docs" {
		t.Fatalf("title match = %+v", got)
	}
	if got[0].Summary == "" || strings.Contains(got[0].Summary, "#") || strings.Contains(got[0].Summary, "`") {
		t.Errorf("summary must be a plain-text snippet, got %q", got[0].Summary)
	}

	// A body match, case-insensitively, with the snippet around the hit.
	got = p.Search(ctx, "ALLOWED_HOSTS")
	if len(got) == 0 {
		t.Fatal("allowed_hosts is documented; expected a body match")
	}
	for _, r := range got {
		if !strings.Contains(strings.ToLower(r.Summary), "allowed_hosts") {
			t.Errorf("snippet for %s must contain the match: %q", r.Title, r.Summary)
		}
		if len(r.Summary) > 220 {
			t.Errorf("snippet for %s too long (%d): %q", r.Title, len(r.Summary), r.Summary)
		}
	}

	// The index page links to the page's base, not "/docs/".
	got = p.Search(ctx, "Overview")
	if len(got) == 0 || got[0].URL != "/docs" {
		t.Errorf("index page = %+v", got)
	}

	// Links follow a renamed page slug.
	db.Model(&blog.Page{}).Where("page_type = ?", PageType).Update("slug", "guide")
	if got = p.Search(ctx, "Writing a theme"); len(got) == 0 || got[0].URL != "/guide/writing-a-theme" {
		t.Errorf("renamed slug = %+v", got)
	}

	for _, q := range []string{"", "   ", "zzzz-no-such-word"} {
		if got = p.Search(ctx, q); got != nil {
			t.Errorf("%q → %+v", q, got)
		}
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("word ", 60) + "needle here " + strings.Repeat("tail ", 60)
	s := snippet(text, "NEEDLE")
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "needle here") {
		t.Errorf("snippet = %q", s)
	}
	if strings.HasPrefix(s, "…ord") || strings.HasSuffix(s, "tai…") {
		t.Errorf("snippet must cut at word boundaries: %q", s)
	}
	if len(s) > snippetLen+20 {
		t.Errorf("snippet too long: %d", len(s))
	}
	if s := snippet("short text", "zzz"); s != "short text" {
		t.Errorf("no match / short text = %q", s)
	}
	if s := snippet("héllo wörld needle", "needle"); !strings.HasSuffix(s, "needle") || strings.HasPrefix(s, "…") {
		t.Errorf("utf-8 text = %q", s)
	}
}

// TestSnippet_UTF8: cuts never split a rune — a UTF-8 continuation byte
// such as the 0xA0 of a no-break space must not pass for whitespace — and
// a lower-casing that changes byte length (İ → i̇) must not index past
// the text.
func TestSnippet_UTF8(t *testing.T) {
	nbsp := strings.Repeat("a ", 40) // 120 bytes: every other byte is 0xA0
	text := nbsp + " needle " + nbsp
	s := snippet(text, "needle")
	if !utf8.ValidString(s) || !strings.Contains(s, "needle") {
		t.Errorf("snippet split a rune: %q", s)
	}
	dotted := strings.Repeat("İ", 100) + " needle" // lower-cased, 100 bytes longer
	s = snippet(dotted, "needle")
	if !utf8.ValidString(s) || !strings.Contains(s, "needle") {
		t.Errorf("length-changing lower-case: %q", s)
	}
	if s = snippet(strings.Repeat("İ", 200), "İ"); !utf8.ValidString(s) {
		t.Errorf("invalid utf-8: %q", s)
	}
}

// TestSitemap: every docs page is in the sitemap under the page's slug; the
// index is the page itself, not "/docs/".
func TestSitemap(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{}, &blog.PostType{})
	p := New()
	var _ gplugin.Sitemapper = p
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	ctx, _ := renderCtx(t, "/sitemap.xml", "")
	ctx.DB = db
	got := p.Sitemap(ctx)
	if len(got) != len(pages)-1 {
		t.Fatalf("%d urls for %d pages (index excluded): %+v", len(got), len(pages), got)
	}
	locs := map[string]bool{}
	for _, u := range got {
		locs[u.Loc] = true
	}
	for _, want := range []string{"/docs/writing-a-plugin", "/docs/directory-formats"} {
		if !locs[want] {
			t.Errorf("missing %s in %v", want, locs)
		}
	}
	if locs["/docs"] || locs["/docs/"] {
		t.Error("the index is the page row itself, which blog already lists")
	}
}

// TestRenderPage_DescribesItself: each docs page gives the <head> a
// description made of its first words, plain text, at most 160 characters.
func TestRenderPage_DescribesItself(t *testing.T) {
	p := New()
	for _, pg := range pages {
		ctx, _ := renderCtx(t, "/docs/"+pg.Slug, pg.Slug)
		_, data := p.RenderPage(ctx, PageType)
		d, _ := data["meta_description"].(string)
		if d == "" || utf8.RuneCountInString(d) > metaDescriptionLen || strings.ContainsAny(d, "#`*\n") {
			t.Errorf("%s: meta_description (%d chars) = %q", pg.Slug, utf8.RuneCountInString(d), d)
		}
	}
	ctx, _ := renderCtx(t, "/docs/writing-a-plugin", "writing-a-plugin")
	_, data := p.RenderPage(ctx, PageType)
	if d := data["meta_description"].(string); !strings.HasPrefix(d, "A goblog plugin is") && !strings.HasPrefix(d, "This page") {
		t.Errorf("description should be the page's opening words, got %q", d)
	}
}

// TestRenderPage_OneH1: the theme's page heading is the doc's title, so the
// article body does not repeat it as a second <h1>; sections stay <h2>.
func TestRenderPage_OneH1(t *testing.T) {
	p := New()
	ctx, _ := renderCtx(t, "/docs/writing-a-plugin", "writing-a-plugin")
	_, data := p.RenderPage(ctx, PageType)
	if data["page_title"] != "Writing a plugin" {
		t.Errorf("page_title = %v", data["page_title"])
	}
	html := data["plugin_content"].(string)
	if strings.Contains(html, "<h1") {
		t.Errorf("article must not carry its own <h1>:\n%s", html[:400])
	}
	if !strings.Contains(html, `<h2 id=`) {
		t.Error("sections stay <h2>")
	}
}
