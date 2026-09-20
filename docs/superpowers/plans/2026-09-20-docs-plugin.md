# Documentation on goblog.live — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** goblog.live/docs documents how to build and publish plugins and themes, with the API references a builder needs, from markdown that ships inside the goblog binary.

**Architecture:** A compiled-in `docs` plugin (`plugins/docs`, off by default) embeds `content/*.md`, renders each page once at construction with goldmark (GFM, auto heading IDs, unsafe HTML allowed — the content is ours), and serves `/docs` + `/docs/<slug>` through `page_content.html` with a sidebar and a per-page table of contents. Tests render every page, resolve every internal link, and lint the identifiers the pages name against the code. The contract docs move into the content directory; README sections shrink to a link.

**Tech Stack:** Go 1.25, gin, `html/template`, `embed`, **goldmark** (new dependency: `github.com/yuin/goldmark`). No JS.

**Spec:** `docs/superpowers/specs/2026-09-20-docs-plugin-design.md`

## Global Constraints

- Seven pages in this sidebar order and with these slugs/titles/files: `` Overview `overview.md`; `writing-a-plugin` Writing a plugin; `plugin-api` Plugin API reference; `publishing-a-plugin` Publishing a plugin; `writing-a-theme` Writing a theme; `publishing-a-theme` Publishing a theme; `directory-formats` Directory formats. Each page starts with an H1 equal to its title.
- Pages link to each other by relative path (`/docs/plugin-api#exports`), never by absolute goblog.live URL. External links (GitHub repos) are absolute.
- Rendering: goldmark with `extension.GFM`, `parser.WithAutoHeadingID()`, `html.WithUnsafe()`; rendered once at plugin construction; a render error panics (tests catch it first).
- Plugin: name `docs`, display name "Documentation", version "1.0.0", page type `docs`, slug `docs`, title "Docs", nav order 32, single setting `enabled` default `"false"`. `/docs` = Overview; unknown slug declines (`"", nil` → 404).
- Every fact in the content must match the code it describes (exports, JSON shapes, limits, setting keys, file paths) — the sources are named per page; the reviewer checks them.
- `docs/PLUGIN_CONTRACT.md` and `docs/THEME_CONTRACT.md` become one-line pointers; existing links keep working.
- `gofmt -l` clean on touched packages; `go vet ./plugins/docs/`; `go test ./...` green before every commit; commit messages end with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Branch `feat/docs-plugin` off `main`, spec branch `docs/docs-plugin-spec` merged in first.

## File structure

```
plugins/docs/
  render.go, render_test.go        goldmark wrapper: Render(src) (html, headings, error)
  pages.go                         the ordered page manifest
  docs.go, docs_test.go            Plugin: settings, page, OnInit, RenderPage; sidebar/TOC template
  content_test.go                  every page renders; internal links resolve; identifier lint
  templates/page.html              sidebar + TOC + article layout (embedded)
  content/overview.md … directory-formats.md   the seven pages
docs/PLUGIN_CONTRACT.md, docs/THEME_CONTRACT.md   MODIFY → pointers
README.md                          MODIFY — WASM plugins / Theming sections shrink + link
goblog.go                          MODIFY — register the plugin
plugins/directory/templates/listing.html, themes-listing.html   MODIFY — "read the docs" link
go.mod / go.sum                    goldmark
```

---

### Task 1: goldmark wrapper — `Render`

**Files:**
- Create: `plugins/docs/render.go`, `plugins/docs/render_test.go`
- Modify: `go.mod`, `go.sum` (`go get github.com/yuin/goldmark@latest`)

**Interfaces:**
- Produces:
  ```go
  package docs
  type Heading struct{ Level int; ID, Text string }
  func Render(src []byte) (template.HTML, []Heading, error)   // GFM + auto heading IDs + unsafe HTML; headings = H2/H3 in document order
  ```

- [ ] **Step 1: Write the failing test**

```go
package docs

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	src := []byte("# Title\n\nIntro with `code` and a [link](/docs/plugin-api#exports).\n\n## Exports\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n### Identity export\n\n<table><tr><td>raw</td></tr></table>\n\n## Limits\n")
	html, heads, err := Render(src)
	if err != nil {
		t.Fatal(err)
	}
	out := string(html)
	for _, want := range []string{`<h1 id="title">Title</h1>`, `<h2 id="exports">Exports</h2>`, `<h3 id="identity-export">Identity export</h3>`, `<table>`, `<td>raw</td>`, `<code>code</code>`, `href="/docs/plugin-api#exports"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	want := []Heading{{2, "exports", "Exports"}, {3, "identity-export", "Identity export"}, {2, "limits", "Limits"}}
	if len(heads) != len(want) {
		t.Fatalf("headings = %+v", heads)
	}
	for i := range want {
		if heads[i] != want[i] {
			t.Errorf("heading %d = %+v, want %+v", i, heads[i], want[i])
		}
	}
}

func TestRender_HeadingTextWithInlineCode(t *testing.T) {
	_, heads, err := Render([]byte("## The `identity` export\n"))
	if err != nil || len(heads) != 1 || heads[0].Text != "The identity export" || heads[0].ID != "the-identity-export" {
		t.Errorf("heads = %+v, %v", heads, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd ~/dev/goblog && go get github.com/yuin/goldmark@latest && go test ./plugins/docs/`
Expected: FAIL — `undefined: Render`.

- [ ] **Step 3: Write `render.go`**

```go
// Package docs serves goblog's builder documentation — how to write and
// publish plugins and themes — from markdown embedded in the binary, so a
// site always documents the version it runs.
package docs

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Heading is one H2/H3 of a rendered page, for its table of contents.
type Heading struct {
	Level int
	ID    string
	Text  string
}

// md is configured once: GitHub-flavoured markdown (tables, strikethrough,
// autolinks), stable heading IDs so sections are linkable, and raw HTML
// allowed — the content is ours and embedded, never user-supplied.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// Render converts one page to HTML and lists its H2/H3 headings in order.
func Render(src []byte) (template.HTML, []Heading, error) {
	doc := md.Parser().Parse(text.NewReader(src))
	var heads []Heading
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		id, _ := h.AttributeString("id")
		heads = append(heads, Heading{Level: h.Level, ID: string(id.([]byte)), Text: plainText(h, src)})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", nil, err
	}
	return template.HTML(buf.String()), heads, nil
}

// plainText flattens a heading's inline children (text, code spans,
// emphasis) to the words a table of contents shows.
func plainText(n ast.Node, src []byte) string {
	var buf bytes.Buffer
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch t := c.(type) {
			case *ast.Text:
				buf.Write(t.Segment.Value(src))
			case *ast.CodeSpan:
				walk(t)
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return buf.String()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./plugins/docs/ -v`
Expected: PASS ×2. If the `id` attribute assertion panics (goldmark returns `[]byte`), keep `string(id.([]byte))`; if a future goldmark returns `string`, switch to a type switch — note it in the report.

- [ ] **Step 5: Commit**

```bash
go mod tidy && git add go.mod go.sum plugins/docs && git commit -m "docs: goldmark renderer with heading IDs and a heading list

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The `docs` plugin — manifest, page, sidebar/TOC layout, skeleton content

**Files:**
- Create: `plugins/docs/pages.go`, `plugins/docs/docs.go`, `plugins/docs/docs_test.go`, `plugins/docs/templates/page.html`, `plugins/docs/content/{overview,writing-a-plugin,plugin-api,publishing-a-plugin,writing-a-theme,publishing-a-theme,directory-formats}.md` (skeletons: H1 + one sentence + a "Coming next" note that Tasks 3–6 replace)
- Modify: `goblog.go` (register)

**Interfaces:**
- Consumes: `Render` (Task 1); `gplugin.BasePlugin`, `gplugin.PageDefinition`, `gplugin.SettingDefinition`, `gplugin.HookContext`, `blog.Page` (as `plugins/directory` uses them).
- Produces:
  ```go
  const PageType = "docs"
  type page struct{ Slug, Title, File string }
  var pages = []page{...}                        // sidebar order; Slug "" = index
  func New() *Plugin                             // renders every page; panics on a render error
  func (p *Plugin) Name/DisplayName/Version/Settings/Pages/OnInit/RenderPage
  func (p *Plugin) rendered(slug string) (renderedPage, bool)   // test seam
  ```

- [ ] **Step 1: Write the failing tests**

```go
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

func TestTOC_OnlyForLongerPages(t *testing.T) {
	p := New()
	short, _ := p.rendered("")
	if len(short.TOC) > 3 && !strings.Contains(sidebarAndArticle("/docs", "", short), `class="docs-toc"`) {
		t.Error("pages with more than three headings get a table of contents")
	}
}
```

(`TestTOC_OnlyForLongerPages` is deliberately tolerant of the skeleton content; it becomes meaningful once Task 4 lands.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/docs/`
Expected: FAIL — undefined `New`, `pages`, …

- [ ] **Step 3: Write `pages.go`**

```go
package docs

// page is one documentation page: its URL segment under /docs, the sidebar
// title (also the page's H1), and its file in content/.
type page struct {
	Slug, Title, File string
}

// pages is the sidebar, in order. Slug "" is the index.
var pages = []page{
	{"", "Overview", "overview.md"},
	{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
	{"plugin-api", "Plugin API reference", "plugin-api.md"},
	{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
	{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
	{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
	{"directory-formats", "Directory formats", "directory-formats.md"},
}
```

- [ ] **Step 4: Write `docs.go`**

```go
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
// first; this is the fallback). A foreign page on the "docs" slug is left
// alone with a log line, as the directory plugin does, so Init continues.
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
		"Items": items,
		"TOC":   r.TOC,
		"HasTOC": len(r.TOC) > 3,
		"HTML":  r.HTML,
	})
	if err != nil {
		log.Printf("Docs plugin: render layout: %v", err)
		return string(r.HTML)
	}
	return buf.String()
}
```

`templates/page.html`:

```html
<div class="row g-4 docs">
  <nav class="col-md-3" aria-label="Documentation">
    <ul class="nav flex-column position-sticky" style="top: 1rem">
      {{ range .Items }}
      <li class="nav-item">{{ if .Current }}<a class="nav-link active" aria-current="page" href="{{ .Href }}">{{ .Title }}</a>{{ else }}<a class="nav-link" href="{{ .Href }}">{{ .Title }}</a>{{ end }}</li>
      {{ end }}
    </ul>
  </nav>
  <article class="col-md-9 docs-article">
    {{ if .HasTOC }}
    <nav class="docs-toc small mb-4" aria-label="On this page">
      <strong>On this page</strong>
      <ul class="list-unstyled mb-0">
        {{ range .TOC }}<li{{ if eq .Level 3 }} class="ms-3"{{ end }}><a href="#{{ .ID }}">{{ .Text }}</a></li>{{ end }}
      </ul>
    </nav>
    {{ end }}
    {{ .HTML }}
  </article>
</div>
```

Skeleton content — each file exactly:

```markdown
# <Title>

<One sentence: what this page will cover.>
```

with the titles from `pages` (the H1 must equal the title, so the auto ID is e.g. `plugin-api-reference`).

Register in `goblog.go` next to `directory`: `registry.Register(docs.New())` (import `"goblog/plugins/docs"`).

- [ ] **Step 5: Run**

Run: `gofmt -l plugins/docs; go vet ./plugins/docs/; go test ./plugins/docs/ -v; go build ./... && go test ./...`
Expected: all PASS. If `aria-current="page"` isn't found, check the template's `if .Current` branch renders the attribute exactly as the test expects.

- [ ] **Step 6: Commit**

```bash
git add plugins/docs goblog.go && git commit -m "docs: compiled-in documentation plugin serving embedded pages at /docs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Content — Overview and Writing a plugin

**Files:**
- Modify: `plugins/docs/content/overview.md`, `plugins/docs/content/writing-a-plugin.md`
- Create: `plugins/docs/content_test.go` (link check; extended in Task 7)

**Sources (read before writing; every fact must match):** `README.md` §Plugins/§WebAssembly plugins/§Dynamic plugins/§Plugin directory; `~/dev/goblog-plugin-hello` (`main.go`, `goblog-plugin.json`, `.github/workflows/release.yml`, README); `plugin/wasm/testdata/echo/main.go`; `docs/PLUGIN_CONTRACT.md`.

- [ ] **Step 1: Link-check test (`content_test.go`)**

```go
package docs

import (
	"regexp"
	"strings"
	"testing"
)

// internalLink matches /docs/<slug>[#anchor] links in rendered HTML.
var internalLink = regexp.MustCompile(`href="/docs(?:/([a-z0-9-]+))?(?:#([A-Za-z0-9_-]+))?"`)

// TestInternalLinksResolve fails when a page links to a slug that is not
// in the manifest or to an anchor that is not a heading on that page.
func TestInternalLinksResolve(t *testing.T) {
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		for _, m := range internalLink.FindAllStringSubmatch(string(r.HTML), -1) {
			slug, anchor := m[1], m[2]
			target, ok := p.rendered(slug)
			if !ok {
				t.Errorf("%s links to unknown page /docs/%s", pg.File, slug)
				continue
			}
			if anchor == "" {
				continue
			}
			if !strings.Contains(string(target.HTML), ` id="`+anchor+`"`) {
				t.Errorf("%s links to /docs/%s#%s but %s has no such heading", pg.File, slug, anchor, target.File)
			}
		}
	}
}

// TestNoAbsoluteSelfLinks keeps a self-hosted copy self-contained.
func TestNoAbsoluteSelfLinks(t *testing.T) {
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		if strings.Contains(string(r.HTML), "goblog.live/docs") {
			t.Errorf("%s links to goblog.live/docs; use /docs/<slug>", pg.File)
		}
	}
}
```

- [ ] **Step 2: Write `overview.md`**

Structure (H1 then these H2s, in this order): `# Overview` · intro paragraph: goblog can be extended two ways — **plugins** (WebAssembly modules, sandboxed, installed from the directory with one click) and **themes** (a directory of templates and CSS layered on the default theme) — and goblog.live hosts a curated directory of both. · `## Plugins` — what a plugin can do (settings, head/footer HTML, its own pages, scheduled jobs, a persistent KV store, HTTP to declared hosts), how it runs (Extism/wasm, no filesystem, network only to `allowed_hosts`, 64 MB, call timeouts), one paragraph on why WASM (any language with a PDK, dependencies bundled, no goblog rebuild). Links: `/docs/writing-a-plugin`, `/docs/plugin-api`. · `## Themes` — templates override `themes/default` by name, ship only what you change, static files with fallback, installed into `themes/installed/`. Link `/docs/writing-a-theme`. · `## Trust model` — a plugin is sandboxed (list the fences); a theme is code with full control of every page including the admin; a directory listing is a maintainer's approval, not an audit; install only what you trust. · `## The directory` — submit → validated on the spot → queued → maintainer approves → listed; `/plugins/index.json` and `/themes/index.json` are what every goblog's installer reads; private directories via `plugin_directory_url`/`theme_directory_url`. Links to `/docs/publishing-a-plugin`, `/docs/publishing-a-theme`, `/docs/directory-formats`. · `## Reference implementations` — bullet links: goblog-plugin-hello, goblog-plugin-scholar, goblog-theme-forest, `plugin/wasm/testdata/echo/main.go` (absolute GitHub URLs).

- [ ] **Step 3: Write `writing-a-plugin.md`**

Structure: `# Writing a plugin` · intro: this walks through `goblog-plugin-hello`, the smallest complete plugin (a footer greeting with one setting); you need Go 1.24+ (wasip1 target) and `github.com/extism/go-pdk`. · `## The manifest` — `goblog-plugin.json` from hello verbatim, then one line per field (name rule `^[a-z0-9-]+$` and equal to `identity`'s name; `runtime` must be `wasm`; `entry` defaults to `plugin.wasm`; `allowed_hosts`; `min_goblog_version` plain semver, wasm needs ≥ 0.2.9). · `## Exports` — explain the model: every export is a Go function marked `//go:wasmexport <name>`, reads JSON via `pdk.Input()`, writes via `pdk.OutputJSON`/`pdk.OutputString`, returns 0 (or sets an error with `pdk.SetErrorString` and returns 1); only `identity` is mandatory. Show hello's `identity`, `settings` and `template_footer` functions verbatim (from `main.go`) with a sentence each; call out the escaping comment (settings are admin-typed but the browser trusts the output). Link to `/docs/plugin-api` for the full list. · `## Build` — the exact command from hello's header comment; note `-ldflags="-s -w"` and the 16 MiB limit; the `//go:build wasip1` + `main_native.go` trick for native tests (from the scholar repo, one paragraph). · `## Validate` — `goblog validate-plugin plugin.wasm` and its JSON output (README's example), plus the Docker form. · `## Run it locally` — copy `plugin.wasm` to `plugins/wasm/hello.wasm` and, if it needs the network, `plugins/wasm/hello.json` with `{"allowed_hosts": [...]}`; restart; enable under Admin → Settings; where the footer appears. · `## Next` — pages, jobs and the store (`/docs/plugin-api#pages`, `#jobs`, `#store`), publishing (`/docs/publishing-a-plugin`).

- [ ] **Step 4: Run and commit**

Run: `go test ./plugins/docs/ -v` → PASS (link check included).
```bash
git add plugins/docs && git commit -m "docs: Overview and Writing a plugin

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Content — Plugin API reference

**Files:**
- Modify: `plugins/docs/content/plugin-api.md`

**Sources:** `README.md` §WebAssembly plugins (the two tables and the limits paragraph are the starting text); `plugin/wasm/contract.go` (exact JSON shapes); `plugin/wasm/wasm.go` (`callTimeout` 10 s, `jobTimeout` 120 s, `memoryPages` 1024 = 64 MB, `maxHTTPResponseBytes`, the busy-wait/revive behaviour in the comments); `plugin/wasm/host.go` (store host functions and their return conventions); `plugin/store.go` (`MaxStoreKeyBytes` 256, `MaxStoreValueBytes` 1 MiB, `MaxStorePluginBytes` 16 MiB, `MaxStorePluginRows` 10000); `plugin/registry.go` (`TemplateData` lands under `.plugins.<name>`; `RenderPage` handled-by-writing rule); `plugin/wasm/testdata/echo/main.go` for every example.

- [ ] **Step 1: Write `plugin-api.md`**

Structure: `# Plugin API reference` · intro: how calls work (JSON in/out via Extism; the export table; `identity` mandatory, everything else optional and no-op when absent). · `## Exports` — the README table, then one H3 per export in this order with the exact input and output JSON (copy field names from `contract.go`), what goblog does with the result, and a short Go snippet from echo: `### identity`, `### settings` (types `text`/`textarea`/`password`; `enabled` convention — the registry treats `enabled=false` as off and skips `run_job`), `### pages`, `### jobs` (`interval_seconds` ≤ 0 → 1 h), `### template_head and template_footer` (HTML string; escaped-by-you), `### template_data` (object available as `.plugins.<name>` in templates), `### render_page` (the three result forms, `sub_path`, returning nothing = 404), `### run_job`, `### on_init`. · `## ctx` — the `hookInput` shape verbatim with field notes. · `## Host functions` — `### store` (four functions, return conventions from `host.go`'s comment, the four limits), `### logging` (`pdk.Log`), `### http` (Extism `http_request`, `allowed_hosts`, redirects checked per hop, response size cap). · `## Limits and lifecycle` — the README limits paragraph rewritten as bullets: per-call timeouts, memory, one instance per plugin with serialised calls, 2 s busy wait for template hooks, timeout closes the instance and it is re-created at most once per 30 s, what Admin → Plugins shows (`busy`/`closed`). · `## Sidecar and loading` — `plugins/wasm/<name>.wasm` + `<name>.json`, `ENABLE_WASM_PLUGINS`. · `## Worked example: Scholar` — three short paragraphs on how goblog-plugin-scholar uses `pages` + `render_page`, `jobs` + `run_job`, and the store (link to the repo).

Use raw HTML `<table>` only if a markdown table would exceed ~120 characters per row; otherwise markdown tables.

- [ ] **Step 2: Run and commit**

Run: `go test ./plugins/docs/ -v` → PASS.
```bash
git add plugins/docs && git commit -m "docs: Plugin API reference

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Content — Publishing a plugin, Publishing a theme; contract pointers

**Files:**
- Modify: `plugins/docs/content/publishing-a-plugin.md`, `plugins/docs/content/publishing-a-theme.md`, `docs/PLUGIN_CONTRACT.md`, `docs/THEME_CONTRACT.md`

**Sources:** the two contract docs (move their content; keep every rule); `plugins/directory/registry/manifest.go` (license list), `theme.go` (reserved names, `MaxScreenshotBytes`, `MaxArchiveEntries`, `MaxTemplateBytes`, `DetectKind`), `plugins/directory/service.go` (rate limit 5/h/IP, `ErrNameTaken` etc.), `plugins/directory/templates/submit.html` (what the form says).

- [ ] **Step 1: Write both pages**

`publishing-a-plugin.md`: `# Publishing a plugin` · intro (what the directory is, that submission is validated on the spot and reviewed by a maintainer, that kind is detected from the manifest). · `## What the repository must contain` — the contract's table and manifest section, updated: include the full known-license list from `manifest.go`. · `## Releases` — contract text + the release workflow YAML from `goblog-plugin-hello/.github/workflows/release.yml`. · `## Submit` — paste the URL at `/plugins/submit` (relative link), what validation does (mirrors `ValidateEntry` step by step), the rate limit, "queued for review", what the maintainer sees (README, allowed hosts), how updates are picked up (every `refresh_minutes`, new tag → rebuilt; failed rebuild keeps the old listing). · `## Common validation errors` — a table of the actual error texts from `manifest.go`/`validate.go` and what to do.

`publishing-a-theme.md`: same shape from `THEME_CONTRACT.md`: `# Publishing a theme` · `## What the repository must contain` (table incl. 256 KiB per template, ≤ 1 MiB screenshot, ≤ 16 MiB / 2000-entry archive, no symlinks) · `## The manifest` (reserved names from `theme.go`; `min_goblog_version` ≥ 0.5.0 is enforced) · `## Releases` (tag zipball, no workflow; content hash explained) · `## Submit` (`/themes/submit`) · `## Common validation errors`.

Replace both `docs/*_CONTRACT.md` with:

```markdown
# Publishing a goblog plugin

Moved: this page is now maintained as goblog's built-in documentation — read it at [goblog.live/docs/publishing-a-plugin](https://www.goblog.live/docs/publishing-a-plugin), or in the source at [`plugins/docs/content/publishing-a-plugin.md`](../plugins/docs/content/publishing-a-plugin.md).
```
(and the theme equivalent).

- [ ] **Step 2: Update in-repo references**

`grep -rn "PLUGIN_CONTRACT\|THEME_CONTRACT" --include=*.go --include=*.html --include=*.md .` — the registry's error message (`manifest.go`: "see docs/PLUGIN_CONTRACT.md") and the submit template's contract link should point at `/docs/publishing-a-plugin` / `/docs/publishing-a-theme` (relative, since the directory site serves the docs); README links likewise. `DetectKind`'s "see docs/…" error text → "see the publishing docs".

- [ ] **Step 3: Run and commit**

Run: `go test ./plugins/... ` → PASS (the registry test that matched "docs/PLUGIN_CONTRACT.md", if any, is updated).
```bash
git add plugins docs README.md && git commit -m "docs: Publishing a plugin / a theme; contract docs move into the plugin

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Content — Writing a theme, Directory formats

**Files:**
- Modify: `plugins/docs/content/writing-a-theme.md`, `plugins/docs/content/directory-formats.md`

**Sources:** `theme/load.go` (`Load` order, `ValidateFiles`, `StaticHandler` fallback), `theme/theme.go` (roots, `THEMES_INSTALLED_DIR`), `README.md` §Theming, `themes/default/templates/` (list the templates and what data each gets — `header.html`/`footer.html` get `.settings`, `.nav_pages`, `.is_admin`, `.logged_in`; `home.html`/`post.html`/`posts.html` get `.posts`/`.post`; `page_content.html` gets `.page`/`.plugin_content`; check each in `blog/blog.go`'s render calls), `goblog.go` (`rawHTML` func), `~/dev/goblog-theme-forest`; `plugins/directory/registry/build.go` (`IndexEntry`, `DetailDoc`, `ReleaseDoc` json tags), `theme.go` (`ContentHash`, `download_url`/`screenshot_url` forms), `plugin/installer/installer.go` and `theme/installer/installer.go` (what each verifies), `tools/migrate.go` (setting defaults).

- [ ] **Step 1: Write `writing-a-theme.md`**

`# Writing a theme` · intro: a theme is `templates/` + optional `static/`, loaded on top of `themes/default` — override only what you change. · `## How loading works` — shared → default → yours, by file name; a template you don't ship renders from default; `/theme/<file>` falls back to default's `static/`; Go `html/template`, `rawHTML` available, `{{ template "name" . }}` across files. · `## A minimal theme` — `header.html` copied from default and edited + `static/css/goblog.css` with a body rule; the tree; `goblog-theme.json`. · `## Template data` — a table: template → the data keys it receives (from `blog.go`), with the caveat that this is the running version's contract. · `## Try it locally` — `themes/installed/<name>/` (or `THEMES_INSTALLED_DIR`), set the `theme` setting, hot-reload on save of the setting; also that Admin → Themes lists it under Installed. · `## Pitfalls` — an empty file does not blank a default template; undefined `{{template}}` references fail at render, not at validation; 256 KiB per file; keep admin/wizard pages to default unless you must (they change with releases). · `## Next` — `/docs/publishing-a-theme`.

- [ ] **Step 2: Write `directory-formats.md`**

`# Directory formats` · intro: goblog.live publishes two indexes; any goblog's installer reads them; you can host your own. · `## index.json` — URL for each kind; JSON array; a field table from `IndexEntry`'s json tags with type, meaning, and plugin/theme differences (`install_type` `wasm`|`theme`; `runtime` `wasm`|empty; `allowed_hosts` `[]` for themes; `download_url` release asset vs tag zipball; `sha256` file hash vs content hash; `screenshot_url` themes only; `kind`). Example entries: hello (from the live index shape) and forest. · `## <name>.json` — `DetailDoc` fields (`readme_html`, `changelog_html`, `releases[]` with `ReleaseDoc` fields), that the HTML is GitHub-rendered. · `## How installers verify` — plugins: sha256 of the asset, load in the sandbox, name/version match, then write; themes: `ContentHash` algorithm (sorted `path\0len\0bytes`), parse templates, then unpack; both refuse on mismatch and never write. · `## Running a private directory` — enable the `directory` plugin on your goblog, add repos in Admin → Plugins → Directory, point other goblogs' `plugin_directory_url` / `theme_directory_url` at it; caution about trust. · `## Caching` — `Cache-Control: public, max-age=300`; installers refresh hourly or on ↻.

- [ ] **Step 3: Run and commit**

Run: `go test ./plugins/docs/ -v` → PASS.
```bash
git add plugins/docs && git commit -m "docs: Writing a theme; Directory formats

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Identifier lint test

**Files:**
- Modify: `plugins/docs/content_test.go`, `plugin/wasm/wasm.go` (export the export-name list)

**Interfaces:**
- Produces: `wasm.Exports []string` (the list currently inlined in `LoadBytes`: identity, settings, pages, jobs, template_head, template_footer, template_data, render_page, run_job, on_init) and `wasm.HostFunctions = []string{"store_get", "store_set", "store_delete", "store_list"}`.

- [ ] **Step 1: Export the lists in `plugin/wasm/wasm.go`**

Replace the inline `[]string{...}` in `LoadBytes` with a package var:

```go
// Exports are the functions a plugin module may export; goblog calls the
// ones present. The documentation is linted against this list.
var Exports = []string{"identity", "settings", "pages", "jobs", "template_head", "template_footer", "template_data", "render_page", "run_job", "on_init"}

// HostFunctions are what goblog offers in the extism:host/user namespace
// besides the PDK's own logging and http_request.
var HostFunctions = []string{"store_get", "store_set", "store_delete", "store_list"}
```

and `for _, name := range Exports {` in `LoadBytes`. `go test ./plugin/wasm/` stays green.

- [ ] **Step 2: The lint**

Append to `content_test.go`:

```go
import (
	"reflect"

	"goblog/plugin/wasm"
	"goblog/plugins/directory/registry"
)

// jsonTags collects the json field names of a struct type (embedded structs
// included), so the docs' field tables are checked against the real shapes.
func jsonTags(t reflect.Type, into map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			jsonTags(f.Type, into)
			continue
		}
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
			into[tag] = true
		}
	}
}

// knownIdentifiers is every snake_case name the docs may put in a code span:
// wasm exports and host functions, the JSON fields of the wire types, and
// an explicit allowlist for setting keys, env vars and file names. A rename
// in code without a docs change fails TestSnakeCaseIdentifiersExist; a new
// term in the docs that is not in code fails it too, which is the point.
func knownIdentifiers() map[string]bool {
	known := map[string]bool{}
	for _, e := range wasm.Exports {
		known[e] = true
	}
	for _, h := range wasm.HostFunctions {
		known[h] = true
	}
	for _, t := range []reflect.Type{reflect.TypeOf(registry.IndexEntry{}), reflect.TypeOf(registry.DetailDoc{}), reflect.TypeOf(registry.ReleaseDoc{}), reflect.TypeOf(registry.ThemeManifest{}), reflect.TypeOf(registry.Manifest{})} {
		jsonTags(t, known)
	}
	for _, w := range wasm.ContractFieldNames() {
		known[w] = true
	}
	for _, s := range []string{
		// settings and env vars named in the docs
		"plugin_directory_url", "theme_directory_url", "refresh_minutes", "github_token", "site_url",
		"ENABLE_WASM_PLUGINS", "ENABLE_DYNAMIC_PLUGINS", "THEMES_INSTALLED_DIR",
		// Extism / PDK names
		"http_request", "go_pdk", "wasmexport",
		// template data keys
		"nav_pages", "is_admin", "logged_in", "has_plugin_content", "plugin_content", "plugin_head_html", "plugin_footer_html",
	} {
		known[s] = true
	}
	return known
}

var snakeCase = regexp.MustCompile(`<code>([a-z][a-z0-9]*(?:_[a-z0-9]+)+)</code>`)

func TestSnakeCaseIdentifiersExist(t *testing.T) {
	known := knownIdentifiers()
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		for _, m := range snakeCase.FindAllStringSubmatch(string(r.HTML), -1) {
			if !known[m[1]] {
				t.Errorf("%s names `%s`, which is not an export, host function, wire field or allowlisted name — renamed in code, or a typo?", pg.File, m[1])
			}
		}
	}
	// And the reference page must mention every export and host function.
	api, _ := p.rendered("plugin-api")
	for _, name := range append(append([]string{}, wasm.Exports...), wasm.HostFunctions...) {
		if !strings.Contains(string(api.HTML), "<code>"+name+"</code>") {
			t.Errorf("plugin-api.md does not document %s", name)
		}
	}
}
```

`wasm.ContractFieldNames()` is a small exported helper in `plugin/wasm/contract.go` returning the json tags of `Identity`, `settingDef`, `pageDef`, `jobDef`, `requestCtx`, `hookInput`, `jobInput`, `initInput`, `rawResponse`, `renderResult` (implemented with the same `jsonTags` walk, since those types are unexported):

```go
// ContractFieldNames lists the JSON field names of every wire type in the
// plugin contract, for the documentation lint.
func ContractFieldNames() []string {
	var out []string
	for _, v := range []any{Identity{}, settingDef{}, pageDef{}, jobDef{}, requestCtx{}, hookInput{}, jobInput{}, initInput{}, rawResponse{}, renderResult{}} {
		t := reflect.TypeOf(v)
		for i := 0; i < t.NumField(); i++ {
			if tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); tag != "" && tag != "-" {
				out = append(out, tag)
			}
		}
	}
	return out
}
```

- [ ] **Step 3: Run; fix the docs, not the lint**

Run: `go test ./plugins/docs/ -run 'Identifiers' -v`. Every failure is either a typo/rename in a page (fix the page) or a legitimate name the allowlist lacks (add it, with a comment on where it comes from). Then `go test ./...`.

- [ ] **Step 4: Commit**

```bash
git add plugins/docs plugin/wasm && git commit -m "docs: lint identifiers in the pages against the plugin contract and wire types

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: README trims, directory footer links, spec tie-in; PR

**Files:**
- Modify: `README.md`, `plugins/directory/templates/listing.html`, `plugins/directory/templates/themes-listing.html`, `plugins/directory/directory_test.go` (if it asserts the footer text)

- [ ] **Step 1: README**

- §WebAssembly plugins: keep the first paragraph, the sidecar/loading paragraph, `validate-plugin`, and "Installing from the directory"; replace the export table, `ctx`, host-function table and limits paragraph with: "The full contract — every export's input and output, `ctx`, host functions, store limits, timeouts — is documented at [goblog.live/docs/plugin-api](https://www.goblog.live/docs/plugin-api) (or `/docs/plugin-api` on any goblog with the `docs` plugin enabled); the source of those pages is `plugins/docs/content/`."
- §Theming: keep the tree and the two-step "create a custom theme"; add "Full guide: [goblog.live/docs/writing-a-theme](https://www.goblog.live/docs/writing-a-theme)."
- §Plugin directory: link `/docs/publishing-a-plugin` and `/docs/directory-formats`.
- Features list: add "Built-in documentation plugin (`docs`): the plugin/theme builder docs served at `/docs`; what goblog.live/docs runs."

- [ ] **Step 2: Directory footers**

`listing.html`: "Built a plugin? <a href="{{ .Base }}/submit">Submit it to the directory</a> — or <a href="/docs/writing-a-plugin">read how to write one</a>." `themes-listing.html`: same with `/docs/writing-a-theme`. (Absolute-path `/docs/...` is right here: the link is from one plugin's page to another plugin's page on the same site.)

- [ ] **Step 3: Verify, commit, PR**

Run: `gofmt -l plugins plugin; go vet ./plugins/... ./plugin/...; go test ./...` → green. Start the app with a scratch sqlite `.env` from the repo root (as the earlier theme work did: build to a scratch dir, symlink `themes`, `templates`, `www`), enable the plugin (`insert into plugin_settings (plugin_name,key,value) values ('docs','enabled','true')`), restart, `curl -s localhost:7000/docs | head -50` shows the sidebar and the Overview H1; `/docs/plugin-api` shows the TOC; `/docs/nope` is 404. Stop it.

```bash
git add -A README.md plugins && git commit -m "Docs plugin: README trims, directory pages link to the docs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/docs-plugin
gh pr create --base main --title "Built-in documentation plugin: goblog.live/docs" --body "$(cat <<'BODY'
Spec `docs/superpowers/specs/2026-09-20-docs-plugin-design.md`.

- New compiled-in `docs` plugin (off by default) serving seven pages at `/docs` from markdown embedded in the binary, rendered with goldmark (new dependency): Overview, Writing a plugin, Plugin API reference, Publishing a plugin, Writing a theme, Publishing a theme, Directory formats. Sidebar + per-page table of contents; no JS.
- Tests render every page, resolve every internal link/anchor, and lint every snake_case identifier the pages name against the wasm exports, host functions and wire-type JSON fields — a rename in code fails CI until the docs follow.
- `docs/PLUGIN_CONTRACT.md` / `THEME_CONTRACT.md` are now pointers; their content lives in the plugin. README's WASM and Theming sections link to the docs instead of duplicating the reference.

After release: enable `docs` on goblog.live (Admin → Settings → Documentation → enabled) and the Docs nav entry appears.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

## Self-review

- **Spec coverage:** pages/sidebar/H1 rule → T2 manifest + T3–6 content; goldmark config, render-once, panic → T1/T2; TOC for >3 headings → T2 template; RenderPage/404/OnInit/settings → T2; contract moves + pointers + in-repo references → T5; README trims + footer links → T8; tests (render, links, sidebar, headings, identifier lint) → T2/T3/T7; rollout (enable on goblog.live) → T8 PR body. Out-of-scope items untouched.
- **Placeholders:** the content tasks give the H1/H2 structure, the facts, and the source files per page; the prose itself is the implementer's — that is the deliverable, and the reviewer checks it against the named sources. Skeleton pages in T2 are explicitly replaced by T3–T6.
- **Type consistency:** `Render`/`Heading` (T1) used by T2; `page`/`pages`/`rendered`/`renderedPage`/`sidebarAndArticle` (T2) used by T3/T7 tests; `wasm.Exports`, `wasm.HostFunctions`, `wasm.ContractFieldNames` (T7) match the lint's use; `registry.IndexEntry/DetailDoc/ReleaseDoc/ThemeManifest/Manifest` exist.
