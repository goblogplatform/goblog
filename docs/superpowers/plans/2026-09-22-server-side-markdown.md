# Server-side markdown rendering — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A post, page or comment leaves the server as HTML, rendered once by goldmark, so crawlers and link-preview bots see the content and the editor preview matches the published page exactly.

**Architecture:** `blog` grows three render methods — `Post.HTML()` (already exists), `Page.HTML()` and `Comment.HTML()` — sharing two goldmark instances: posts and pages keep raw HTML (admin authors, embeds must work), comments go through bluemonday's `UGCPolicy` (anonymous authors). A new admin-only `POST /api/v1/preview` renders unsaved markdown through the post path, and the admin editor gains Write/Preview tabs that call it. The public templates drop showdown and DOMPurify entirely.

**Tech Stack:** Go 1.26, Gin 1.12, gorm, goldmark 1.8.6 (already a dependency), bluemonday (new), SimpleMDE 1.11 (already loaded on admin pages), sqlite/mysql/postgres.

**Spec:** `docs/superpowers/specs/2026-09-22-server-side-markdown-design.md`

## Global Constraints

- **Posts and pages are rendered unsanitised** (goldmark `html.WithUnsafe()`). This is the site owner's explicit decision: embeds, including `<script>`-based ones, must work. Never add a sanitiser to the post/page path.
- **Comments are always sanitised** with `bluemonday.UGCPolicy()`. Never configurable, never relaxed: commenters are anonymous.
- `.post.Content`, `.page.Content` and `.Content` on comments stay available to templates. A theme that has not migrated must keep working.
- Every new template data key must be added to the allowlist in `plugins/docs/content_test.go` (`knownIdentifiers`) if it contains an underscore, or `TestSnakeCaseIdentifiersExist` fails once it is documented.
- Run `gofmt -l <changed files>` before each commit. `blog/blog.go`, `blog/page.go` and `blog/post.go` are already unformatted on main — do not reformat them wholesale; only keep your own additions gofmt-clean.
- Commit messages: sentence-case subject, no `feat:`/`fix:` prefixes (match the existing log), and end with:
  `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

## File Structure

| File | Responsibility |
|---|---|
| `blog/render.go` (new) | The two goldmark instances and the shared render helpers. One place markdown becomes HTML. |
| `blog/render_test.go` (new) | Tests for both policies. |
| `blog/post.go` (modify) | `Post.HTML()` moves to using the shared instance; `postMarkdown` var is deleted. |
| `blog/page.go` (modify) | `Page.HTML()`. |
| `blog/comment.go` (modify) | `Comment.HTML()`. |
| `admin/preview.go` (new) | The `POST /api/v1/preview` handler. |
| `admin/preview_test.go` (new) | Its tests. |
| `goblog.go` (modify) | Route registration for the preview endpoint. |
| `www/js/admin-script.js` (modify) | `setupEditorTabs()` — the Write/Preview tab behaviour, shared by all three admin editors. |
| `themes/default/templates/post.html` (modify) | Render `{{ .post.HTML }}` and `{{ .HTML }}` per comment; drop showdown/DOMPurify/noscript. |
| `themes/default/templates/post-admin.html` (modify) | Same, plus the editor tabs. |
| `themes/default/templates/page_content.html` (modify) | Render `{{ .page.HTML }}`; drop showdown/DOMPurify. |
| `themes/default/templates/admin_new_post.html` (modify) | Editor tabs. |
| `themes/default/templates/admin_edit_page.html` (modify) | Editor tabs. |
| `plugins/docs/content/writing-a-theme.md` (modify) | Document `.post.HTML` / `.page.HTML` / comment `.HTML`, and the unsanitised-HTML property. |

Theme repos (`goblog-site-theme`, `goblog-theme-forest`) are Task 8, after goblog is green.

---

### Task 1: One renderer, two policies

**Files:**
- Create: `blog/render.go`
- Create: `blog/render_test.go`
- Modify: `blog/post.go` (delete `postMarkdown` and the body of `Post.HTML`, lines ~196-212)

**Interfaces:**
- Produces: `blog.renderMarkdown(src string) template.HTML` (unsanitised, package-private) and `blog.renderComment(src string) template.HTML` (sanitised, package-private). Task 2 and Task 3 call these; Task 4 calls `Post.HTML()`.

- [ ] **Step 1: Add the bluemonday dependency**

```bash
cd ~/dev/goblog && go get github.com/microcosm-cc/bluemonday@latest
```

- [ ] **Step 2: Write the failing test**

Create `blog/render_test.go`:

```go
package blog_test

import (
	"strings"
	"testing"

	. "goblog/blog"
)

// TestPostHTML_KeepsRawHTML: posts are written by admins and their embeds —
// YouTube iframes, Instagram scripts — must survive to the page. This is a
// deliberate decision recorded in the design doc; do not add a sanitiser here.
func TestPostHTML_KeepsRawHTML(t *testing.T) {
	p := Post{Content: "Intro\n\n<iframe src=\"https://www.youtube.com/embed/x\"></iframe>\n\n<script async src=\"//www.instagram.com/embed.js\"></script>\n"}
	got := string(p.HTML())
	for _, want := range []string{`<iframe src="https://www.youtube.com/embed/x">`, `<script async src="//www.instagram.com/embed.js">`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestPostHTML_RendersGFM covers the markdown features the old showdown
// configuration had ({tables: true}) plus the rest of GitHub-flavoured
// markdown, so a migrated theme renders at least what the browser did.
func TestPostHTML_RendersGFM(t *testing.T) {
	p := Post{Content: "# Title\n\nSome **bold** and a [link](/x).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n\n- [x] done\n- [ ] todo\n\n~~struck~~\n"}
	got := string(p.HTML())
	for _, want := range []string{"<h1", "<strong>bold</strong>", `<a href="/x">link</a>`, "<table>", "<td>1</td>", "<code", "func main()", `type="checkbox"`, "<del>struck</del>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestCommentHTML_Sanitises: commenters are anonymous, so a comment may use
// ordinary markdown but never raw HTML, scripts, embeds or javascript: URLs.
func TestCommentHTML_Sanitises(t *testing.T) {
	c := Comment{Content: "Nice **post**! See [my site](https://example.test).\n\n<script>alert(1)</script>\n\n<iframe src=\"https://evil.test\"></iframe>\n\n<img src=x onerror=alert(1)>\n\n[click](javascript:alert(1))\n"}
	got := string(c.HTML())
	for _, want := range []string{"<strong>post</strong>", `href="https://example.test"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, gone := range []string{"<script", "alert(1)", "<iframe", "onerror", "javascript:"} {
		if strings.Contains(got, gone) {
			t.Errorf("must not contain %q in %q", gone, got)
		}
	}
}

// TestPageHTML: a page body is authored in the admin like a post, so it
// renders with the same policy.
func TestPageHTML(t *testing.T) {
	p := Page{Content: "## Heading\n\n<iframe src=\"https://maps.example/embed\"></iframe>\n"}
	got := string(p.HTML())
	if !strings.Contains(got, "<h2") || !strings.Contains(got, "<iframe") {
		t.Errorf("page HTML = %q", got)
	}
}

// TestHTML_EmptyContent: nothing to render is an empty body, not an error
// page or a panic.
func TestHTML_EmptyContent(t *testing.T) {
	if got := string((Post{}).HTML()); strings.TrimSpace(got) != "" {
		t.Errorf("empty post = %q", got)
	}
	if got := string((Comment{}).HTML()); strings.TrimSpace(got) != "" {
		t.Errorf("empty comment = %q", got)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./blog/ -run 'TestPostHTML|TestCommentHTML|TestPageHTML|TestHTML_Empty' 2>&1 | head -20`
Expected: build failure — `p.HTML undefined (type Page has no field or method HTML)` and the same for `Comment`. `TestPostHTML_KeepsRawHTML` may already pass, since `Post.HTML` exists from the RSS work.

- [ ] **Step 4: Create the shared renderer**

Create `blog/render.go`:

```go
package blog

import (
	"bytes"
	"html/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// authorMarkdown renders content written in the admin — posts and pages.
// Raw HTML is kept: the authors are admins and their embeds (YouTube
// iframes, Instagram scripts) depend on it. This is a deliberate decision,
// recorded in docs/superpowers/specs/2026-09-22-server-side-markdown-design.md;
// a post or page can therefore run JavaScript on the site.
var authorMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// commentMarkdown renders what visitors write. Raw HTML is dropped by
// goldmark and whatever markdown produces is then filtered by
// commentPolicy, because commenters are anonymous.
var commentMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// commentPolicy keeps ordinary prose — emphasis, lists, code, links — and
// drops everything else, including javascript: URLs and event handlers.
var commentPolicy = bluemonday.UGCPolicy()

// renderMarkdown turns an author's markdown into HTML, raw HTML included.
// A render error falls back to the escaped source, so a broken document
// shows its text rather than an empty page.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := authorMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

// renderComment turns a visitor's markdown into sanitised HTML.
func renderComment(src string) template.HTML {
	var buf bytes.Buffer
	if err := commentMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(commentPolicy.SanitizeBytes(buf.Bytes()))
}
```

- [ ] **Step 5: Point Post.HTML at it and add the other two**

In `blog/post.go`, delete the `postMarkdown` variable and its comment (around lines 196-202) and replace the body of `Post.HTML`:

```go
// HTML is the post's content rendered to HTML on the server: what the page,
// the feed and the editor preview all show.
func (p Post) HTML() template.HTML { return renderMarkdown(p.Content) }
```

Remove the now-unused `goldmark`, `extension` and `html` imports from `blog/post.go` (keep `bytes` and `html/template` — other functions use them; run `go build ./...` to confirm).

In `blog/page.go`, add after the `Page` struct:

```go
// HTML is the page's content rendered to HTML on the server, with the same
// policy as a post: its author is an admin.
func (p Page) HTML() template.HTML { return renderMarkdown(p.Content) }
```

`blog/page.go` needs `"html/template"` in its imports.

In `blog/comment.go`, add after the `Comment` struct:

```go
// HTML is the comment rendered to HTML on the server. Commenters are
// anonymous, so the result is sanitised: ordinary formatting survives, raw
// HTML, scripts and javascript: URLs do not.
func (c Comment) HTML() template.HTML { return renderComment(c.Content) }
```

`blog/comment.go` needs `"html/template"` in its imports.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./blog/ 2>&1 | tail -5`
Expected: `ok goblog/blog`. If `TestPostHTML_RendersGFM` fails on the task-list checkbox, confirm `extension.GFM` is in use — it bundles tables, strikethrough, linkify and task lists.

- [ ] **Step 7: Commit**

```bash
cd ~/dev/goblog
gofmt -l blog/render.go blog/render_test.go blog/comment.go blog/page.go
go test ./... 2>&1 | grep -E "^(FAIL|---)"
git add blog/render.go blog/render_test.go blog/post.go blog/page.go blog/comment.go go.mod go.sum
git commit -m "$(cat <<'EOF'
One markdown renderer: posts and pages raw, comments sanitised

goldmark renders both, sharing one instance each: an author's markdown
keeps raw HTML so admin-written embeds work, a visitor's comment goes
through bluemonday's UGCPolicy because commenters are anonymous.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: The preview endpoint

**Files:**
- Create: `admin/preview.go`
- Create: `admin/preview_test.go`
- Modify: `goblog.go` (route registration, next to the other `/api/v1` POSTs around line 435)

**Interfaces:**
- Consumes: `blog.Post.HTML()` from Task 1.
- Produces: `func (a *Admin) Preview(c *gin.Context)` and the route `POST /api/v1/preview`, which Task 5 calls from the browser. Request `{"content": "<markdown>"}`, response `{"html": "<rendered>"}`.

- [ ] **Step 1: Write the failing test**

Create `admin/preview_test.go`:

```go
package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func previewHarness(t *testing.T, isAdmin bool) *gin.Engine {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Setting{}, &blog.Page{})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(isAdmin)
	a.On("IsLoggedIn", mock.Anything).Return(isAdmin)
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
	router.POST("/api/v1/preview", ad.Preview)
	return router
}

func postPreview(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// TestPreview_RendersLikeThePublishedPost: the editor's preview must be the
// same rendering the page will show, so it goes through blog.Post.HTML.
func TestPreview_RendersLikeThePublishedPost(t *testing.T) {
	router := previewHarness(t, true)
	md := "# Title\n\nSome **bold** text.\n\n<iframe src=\"https://www.youtube.com/embed/x\"></iframe>\n"
	w := postPreview(t, router, `{"content":`+jsonString(md)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if want := string((blog.Post{Content: md}).HTML()); got.HTML != want {
		t.Errorf("preview = %q, want %q", got.HTML, want)
	}
	if !strings.Contains(got.HTML, "<iframe") {
		t.Errorf("an author's embed must survive the preview: %q", got.HTML)
	}
}

// TestPreview_EmptyAndMalformed: an empty document previews as nothing; a
// body that is not the expected shape is a 400, not a panic.
func TestPreview_EmptyAndMalformed(t *testing.T) {
	router := previewHarness(t, true)
	if w := postPreview(t, router, `{"content":""}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"html":""`) {
		t.Errorf("empty: %d %s", w.Code, w.Body.String())
	}
	if w := postPreview(t, router, `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d %s", w.Code, w.Body.String())
	}
}

// TestPreview_AdminOnly: the endpoint renders whatever it is given with the
// unsanitised author policy, so only an admin may call it.
func TestPreview_AdminOnly(t *testing.T) {
	router := previewHarness(t, false)
	w := postPreview(t, router, `{"content":"# hi"}`)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Not Authorized") {
		t.Errorf("non-admin: %d %s", w.Code, w.Body.String())
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./admin/ -run TestPreview 2>&1 | head -10`
Expected: build failure — `ad.Preview undefined (type admin.Admin has no field or method Preview)`.

- [ ] **Step 3: Write the handler**

Create `admin/preview.go`:

```go
package admin

import (
	"net/http"

	"goblog/blog"

	"github.com/gin-gonic/gin"
)

// Preview renders unsaved markdown for the editor's Preview tab, through
// exactly the path the published page uses (blog.Post.HTML), so what an
// author sees is what readers get. Admin only: the renderer keeps raw HTML.
func (a *Admin) Preview(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}
	c.JSON(http.StatusOK, gin.H{"html": string(blog.Post{Content: body.Content}.HTML())})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./admin/ -run TestPreview -v 2>&1 | grep -E "^(=== RUN|--- |ok|FAIL)"`
Expected: three PASS lines.

- [ ] **Step 5: Register the route**

In `goblog.go`, next to the other `/api/v1` routes (near `router.POST("/api/v1/upload", …)`, around line 435), add:

```go
	router.POST("/api/v1/preview", goblog._admin.Preview)
```

Verify it is routed: `go build -o /tmp/goblog-check . && echo built` (the route list is printed at startup; a full manual check comes in Task 7).

- [ ] **Step 6: Commit**

```bash
cd ~/dev/goblog
gofmt -l admin/preview.go admin/preview_test.go goblog.go
go test ./... 2>&1 | grep -E "^(FAIL|---)"
git add admin/preview.go admin/preview_test.go goblog.go
git commit -m "$(cat <<'EOF'
Admin preview endpoint renders unsaved markdown

POST /api/v1/preview renders through blog.Post.HTML, the same path the
published page uses, so the editor's preview cannot drift from it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: The public post page renders on the server

**Files:**
- Modify: `themes/default/templates/post.html` (body at line 27, showdown block at lines ~114-127, comment rendering at lines ~158-176)
- Modify: `blog/blog_test.go` (add the test below)

**Interfaces:**
- Consumes: `Post.HTML()` and `Comment.HTML()` from Task 1.

- [ ] **Step 1: Write the failing test**

Append to `blog/blog_test.go`:

```go
// TestPostPage_ServerRendered: the post body and its comments are HTML in
// the response, so a crawler or link-preview bot that runs no JavaScript
// sees the content; showdown and DOMPurify are gone from the page (#617).
func TestPostPage_ServerRendered(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	when := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	post := blog.Post{Title: "Rendered", Slug: "rendered", PostTypeID: pt.ID, CreatedAt: when, UpdatedAt: when,
		Content: "Some **bold** text.\n\n<iframe src=\"https://www.youtube.com/embed/x\"></iframe>\n"}
	db.Create(&post)
	db.Create(&blog.Comment{PostID: post.ID, Name: "Visitor", Content: "Nice **post**!\n\n<script>alert(1)</script>"})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")

	router := gin.New()
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/posts/:yyyy/:mm/:dd/:slug", b.Post)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/posts/2026/08/15/rendered", nil)
	router.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	for _, want := range []string{"<strong>bold</strong>", `<iframe src="https://www.youtube.com/embed/x">`, "<strong>post</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, gone := range []string{"showdown", "purify", "DOMPurify", "<noscript>"} {
		if strings.Contains(body, gone) {
			t.Errorf("page still references %q", gone)
		}
	}
	if strings.Contains(body, "alert(1)") {
		t.Error("a comment's script must not reach the page")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./blog/ -run TestPostPage_ServerRendered 2>&1 | grep -E "blog_test.go:[0-9]+|FAIL" | head`
Expected: failures for the missing `<strong>bold</strong>` and for `showdown` still being present.

- [ ] **Step 3: Render the body server-side**

In `themes/default/templates/post.html`, replace line 27:

```html
        <div id="html" class="text-left"></div>
```

with:

```html
        <div id="html" class="text-left">{{ .post.HTML }}</div>
```

- [ ] **Step 4: Delete the showdown block and the noscript fallback**

In the same file, delete this whole block (around lines 114-127) — the two `<script src>` tags, the inline script and the `<noscript>` element:

```html
  <script src="https://cdnjs.cloudflare.com/ajax/libs/showdown/2.1.0/showdown.min.js" …></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/dompurify/3.2.7/purify.min.js" …></script>
  <script type="text/javascript">
    var converter = new showdown.Converter({tables: true});
    var md = "{{ .post.Content }}";
    var html = DOMPurify.sanitize(converter.makeHtml(md));
    $("#html").html(html)
  </script>
  <noscript>
    <div class="container">
      <p style="text-align: left;">
        {{ .post.Content }}
      </p>
    </div>
  </noscript>
```

- [ ] **Step 5: Render comments server-side**

In the same file, replace the comment body markup (around lines 158-161):

```html
          <div class="comment-content" id="comment-content-{{ .ID }}"></div>
          <noscript>
            <div class="comment-content">{{ .Content }}</div>
          </noscript>
```

with:

```html
          <div class="comment-content">{{ .HTML }}</div>
```

and delete the per-comment rendering script that follows the `{{ end }}` of the comments range (around lines 165-176):

```html
        <script type="text/javascript">
          {{ range .comments }}
          (function() {
            var raw = {{ .Content }};
            …
          })();
          {{ end }}
        </script>
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./blog/ -run TestPostPage_ServerRendered 2>&1 | tail -3`
Expected: `ok goblog/blog`.

- [ ] **Step 7: Run the whole suite**

Run: `go test ./... 2>&1 | grep -E "^(FAIL|---)"`
Expected: no output. If an existing test asserted on the showdown markup, update it to the new contract rather than restoring the scripts.

- [ ] **Step 8: Commit**

```bash
cd ~/dev/goblog
git add themes/default/templates/post.html blog/blog_test.go
git commit -m "$(cat <<'EOF'
Post pages render their body and comments on the server

The body and each comment are HTML in the response, so anything that does
not run JavaScript — link previews, most crawlers, reader modes — sees the
post. showdown and DOMPurify are gone from the page.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Custom pages render on the server

**Files:**
- Modify: `themes/default/templates/page_content.html` (lines ~25-35)
- Modify: `blog/blog_test.go` (add the test below)

**Interfaces:**
- Consumes: `Page.HTML()` from Task 1.

- [ ] **Step 1: Write the failing test**

Append to `blog/blog_test.go`:

```go
// TestCustomPage_ServerRendered: a custom page's markdown is rendered on the
// server too; plugin pages, which pass their own HTML, are unaffected.
func TestCustomPage_ServerRendered(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, Enabled: true,
		Content: "I write **software**.\n\n<iframe src=\"https://maps.example/embed\"></iframe>\n"})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")

	router := gin.New()
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.NoRoute(b.NoRoute)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/about", nil)
	router.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "<strong>software</strong>") || !strings.Contains(body, "<iframe") {
		t.Errorf("page body not rendered:\n%s", body)
	}
	for _, gone := range []string{"showdown", "purify"} {
		if strings.Contains(body, gone) {
			t.Errorf("page still references %q", gone)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./blog/ -run TestCustomPage_ServerRendered 2>&1 | grep -E "blog_test.go:[0-9]+|FAIL" | head`
Expected: "page body not rendered" and the showdown reference.

- [ ] **Step 3: Render the page server-side**

In `themes/default/templates/page_content.html`, replace the `{{ else }}` branch (the `<div id="page-content"></div>`, the two `<script src>` tags and the inline script, around lines 25-35) so the block reads:

```html
    {{ if .has_plugin_content }}
    <div id="page-content">{{ .plugin_content | rawHTML }}</div>
    {{ else }}
    <div id="page-content">{{ .page.HTML }}</div>
    {{ end }}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./blog/ -run TestCustomPage_ServerRendered 2>&1 | tail -3`
Expected: `ok goblog/blog`.

- [ ] **Step 5: Check the plugin pages still work**

Run: `go test ./plugins/... ./blog/ 2>&1 | grep -E "^(ok|FAIL)"`
Expected: all ok — the directory and docs plugins render through `has_plugin_content`, which the edit above leaves untouched.

- [ ] **Step 6: Commit**

```bash
cd ~/dev/goblog
git add themes/default/templates/page_content.html blog/blog_test.go
git commit -m "$(cat <<'EOF'
Custom pages render their body on the server

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Write/Preview tabs in the editor

**Files:**
- Modify: `www/js/admin-script.js` (add `setupEditorTabs`, near the other editor helpers)
- Modify: `themes/default/templates/admin_new_post.html` (content field around line 40, script at line 65)
- Modify: `themes/default/templates/post-admin.html` (content field, script at line 199)
- Modify: `themes/default/templates/admin_edit_page.html` (textarea at line 70)

**Interfaces:**
- Consumes: `POST /api/v1/preview` from Task 2.
- Produces: `setupEditorTabs(textareaID)` in `admin-script.js`, called by each editor template after its editor is constructed.

- [ ] **Step 1: Add the tab behaviour**

In `www/js/admin-script.js`, add at the end of the file:

```javascript
// setupEditorTabs turns the markup an editor template ships — a .editor-tabs
// bar and a .editor-preview pane beside the textarea — into Write/Preview
// tabs. Preview asks the server to render the unsaved markdown, so it shows
// exactly what the published page will, rather than a second markdown
// dialect running in the browser. simplemde may be undefined (the page
// editor uses a plain textarea).
function setupEditorTabs(textareaID) {
    var tabs = document.querySelector('.editor-tabs[data-editor="' + textareaID + '"]');
    if (!tabs) { return; }
    var writeTab = tabs.querySelector('[data-tab="write"]');
    var previewTab = tabs.querySelector('[data-tab="preview"]');
    var preview = document.getElementById(textareaID + '-preview');
    var editorWrap = document.getElementById(textareaID + '-write');

    function value() {
        if (typeof simplemde !== 'undefined' && simplemde && simplemde.value) {
            return simplemde.value();
        }
        var el = document.getElementById(textareaID);
        return el ? el.value : '';
    }

    function show(which) {
        var previewing = which === 'preview';
        editorWrap.style.display = previewing ? 'none' : '';
        preview.style.display = previewing ? '' : 'none';
        writeTab.classList.toggle('active', !previewing);
        previewTab.classList.toggle('active', previewing);
    }

    writeTab.addEventListener('click', function (e) { e.preventDefault(); show('write'); });
    previewTab.addEventListener('click', function (e) {
        e.preventDefault();
        show('preview');
        preview.textContent = 'Rendering…';
        $.ajax({
            url: '/api/v1/preview',
            type: 'post',
            contentType: 'application/json',
            dataType: 'json',
            data: JSON.stringify({ content: value() }),
            success: function (json) { preview.innerHTML = json.html; },
            error: function (jqXHR, textStatus, errorThrown) {
                var reason = typeof jqXHR.responseJSON === 'string' ? jqXHR.responseJSON : (textStatus + ' ' + errorThrown);
                preview.textContent = 'Could not render the preview: ' + reason;
            }
        });
    });
}
```

- [ ] **Step 2: Add the markup to the new-post editor**

In `themes/default/templates/admin_new_post.html`, replace the content form group (around lines 38-43):

```html
    <div class="form-group">
        <label for="content">Content <span class="require">*</span></label>
        <ul class="nav nav-tabs editor-tabs" data-editor="content">
            <li class="nav-item"><a class="nav-link active" href="#" data-tab="write">Write</a></li>
            <li class="nav-item"><a class="nav-link" href="#" data-tab="preview">Preview</a></li>
        </ul>
        <div class="text-left" id="content-write">
            <textarea rows="5" class="form-control" name="content" id="content"></textarea>
        </div>
        <div class="editor-preview-pane" id="content-preview" style="display: none;"></div>
    </div>
```

- [ ] **Step 3: Construct SimpleMDE without its own preview, and wire the tabs**

In the same file, change the SimpleMDE construction (line ~65) to drop its preview buttons, and call the tab setup after the upload wiring:

```javascript
        var simplemde = new SimpleMDE({
            element: $("#content")[0],
            // One preview mechanism: the Write/Preview tabs, which render
            // through the server. SimpleMDE's own preview uses a different
            // markdown implementation and would disagree.
            toolbar: ["bold", "italic", "heading", "|", "quote", "unordered-list", "ordered-list", "|", "link", "image", "|", "guide"],
            spellChecker: false
        });
```

and at the end of that same `<script>` block (after the `inlineAttachment` call):

```javascript
        setupEditorTabs("content");
```

- [ ] **Step 4: Repeat for the post editor**

In `themes/default/templates/post-admin.html`, apply the same three edits: the tab markup around its content textarea, the SimpleMDE options at line ~199, and `setupEditorTabs("content");` after the `inlineAttachment` call.

- [ ] **Step 5: Repeat for the page editor**

In `themes/default/templates/admin_edit_page.html`, wrap its textarea (line ~70) in the same tab markup:

```html
        <ul class="nav nav-tabs editor-tabs" data-editor="content">
            <li class="nav-item"><a class="nav-link active" href="#" data-tab="write">Write</a></li>
            <li class="nav-item"><a class="nav-link" href="#" data-tab="preview">Preview</a></li>
        </ul>
        <div id="content-write">
            <textarea rows="15" class="form-control" id="content">{{ .page.Content }}</textarea>
        </div>
        <div class="editor-preview-pane" id="content-preview" style="display: none;"></div>
```

and add `setupEditorTabs("content");` to the page's existing inline script (or a new `<script>` before `</body>` if it has none).

- [ ] **Step 6: Give the preview pane a surface**

In `www/css/admin.css`, add:

```css
/* The editor's Preview tab: a reading surface the same size as the editor. */
.editor-preview-pane {
    border: 1px solid #ced4da;
    border-top: 0;
    border-radius: 0 0 0.375rem 0.375rem;
    padding: 12px;
    min-height: 200px;
    background: #fff;
}
.editor-tabs { margin-bottom: 0; }
```

- [ ] **Step 7: Check the admin pages still render**

Run: `go test ./admin/ 2>&1 | grep -E "^(ok|FAIL)"`
Expected: `ok goblog/admin`. The admin page tests parse these templates, so a template syntax error fails here.

- [ ] **Step 8: Commit**

```bash
cd ~/dev/goblog
git add www/js/admin-script.js www/css/admin.css themes/default/templates/admin_new_post.html themes/default/templates/post-admin.html themes/default/templates/admin_edit_page.html
git commit -m "$(cat <<'EOF'
Editor: Write and Preview tabs rendered by the server

SimpleMDE's live preview is gone — it re-rendered as you typed with a
different markdown implementation. The Preview tab asks /api/v1/preview,
so it shows exactly what the published page will.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Document the contract

**Files:**
- Modify: `plugins/docs/content/writing-a-theme.md` (the template data table, and the loading section)
- Modify: `plugins/docs/content_test.go` if the lint rejects a new identifier

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Document the rendered fields**

In `plugins/docs/content/writing-a-theme.md`, in the per-template table, change the `post.html` and `page_content.html` rows to mention the rendered HTML, and add a paragraph after the table:

```markdown
Post, page and comment bodies are rendered to HTML by goblog: use
`{{ .post.HTML }}`, `{{ .page.HTML }}` and, for each comment, `{{ .HTML }}`.
A theme that still renders `.Content` with a markdown library in the browser
keeps working, but its pages are markdown to anything that does not run
JavaScript — crawlers, link previews, reader modes.

A post or page is rendered **without sanitising**: its author is an admin, and
their embeds (a YouTube iframe, an Instagram script) have to survive. Comments
are sanitised, because commenters are not. A theme must therefore treat post
and page HTML as trusted and comment HTML as already-filtered; neither needs
`rawHTML`.
```

- [ ] **Step 2: Run the docs lint**

Run: `go test ./plugins/docs/ 2>&1 | grep -E "^(ok|FAIL)|content_test.go"`
Expected: `ok`. If `TestSnakeCaseIdentifiersExist` complains about a new identifier, add it to the allowlist in `plugins/docs/content_test.go` with a one-line comment saying where it comes from.

- [ ] **Step 3: Commit**

```bash
cd ~/dev/goblog
git add plugins/docs/content/writing-a-theme.md plugins/docs/content_test.go
git commit -m "$(cat <<'EOF'
Docs: themes render .post.HTML, .page.HTML and a comment's .HTML

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Verify against real content, then open the PR

**Files:** none changed unless a defect turns up.

- [ ] **Step 1: Run the whole suite and the formatters**

```bash
cd ~/dev/goblog
gofmt -l blog admin www 2>/dev/null | grep -v "^blog/blog.go$\|^blog/page.go$\|^blog/post.go$"
go vet ./... 2>&1 | grep -v "copies lock\|^#"
go test ./... 2>&1 | grep -E "^(FAIL|---)"
```
Expected: no output from any of them.

- [ ] **Step 2: Run goblog locally against a copy of real content**

```bash
S=/tmp/claude-1000/-home-jason-dev-goblog/*/scratchpad   # this session's scratchpad
cd ~/dev/goblog && go build -o "$S/goblog" .
scp root@www:/opt/goblog/prod/*.db "$S/real.db"          # a copy; never write to prod
printf 'SESSION_KEY=local-dev\ndatabase=sqlite\nsqlite_db=%s/real.db\nclient_id=abc\nclient_secret=def\n' "$S" > .env
(cd ~/dev/goblog && nohup "$S/goblog" > "$S/server.log" 2>&1 &)
sleep 6
```

- [ ] **Step 3: Check a post whose source holds an embed**

```bash
curl -s "http://localhost:7000/posts/2017/02/02/One-Year-in-our-mobile-mesh-platform-is-taking-shape" > "$S/post.html"
grep -c "<iframe" "$S/post.html"          # expect 1 or more — the YouTube embed now survives
grep -c "showdown\|purify" "$S/post.html" # expect 0
grep -c "<strong>\|<h2" "$S/post.html"    # expect > 0 — the body is HTML, not markdown
```

Note: `curl` sees the body now, which is the whole point of the change. Use
`curl -o file -D -` rather than `curl -I`: several goblog routes answer GET
only, so a HEAD request 404s and looks like a bug that is not there.

- [ ] **Step 4: Check a post with comments**

```bash
sqlite3 "$S/real.db" "select p.slug, strftime('%Y/%m/%d', p.created_at) from posts p join comments c on c.post_id = p.id limit 1;"
# then fetch that permalink and confirm the comment body is rendered HTML
```

- [ ] **Step 5: Stop the server and clean up**

```bash
pkill -f "$S/goblog"; rm -f ~/dev/goblog/.env
cd ~/dev/goblog && git status -s   # expect only intended changes; no .env, no *.db
```

- [ ] **Step 6: Open the PR**

```bash
cd ~/dev/goblog
git push -u origin feat/617-server-side-markdown
gh pr create --title "Render post, page and comment markdown on the server" --body "…"
```

The body should state: what changed, that posts and pages are deliberately
unsanitised while comments are sanitised, that `.Content` still works for
un-migrated themes, the embed evidence (4 posts with iframes and 7 with
scripts that DOMPurify strips today), the local verification output from
steps 3-4, and that theme PRs follow in Task 8.

---

### Task 8: The other two themes

**Files (other repos):**
- `~/dev/goblog-site-theme/templates/post.html`, `page_content.html`
- `~/dev/goblog-theme-forest/templates/post.html`, `page_content.html`

Do this only once the goblog PR is merged and released, so the themes never
reference a method the running goblog lacks.

- [ ] **Step 1: Apply the same edits to each theme**

For each repo, on a new branch off `main`, make exactly the Task 3 and Task 4
edits: `{{ .post.HTML }}`, `{{ .page.HTML }}`, `{{ .HTML }}` per comment, and
delete the showdown/DOMPurify script tags and the `<noscript>` fallbacks. Keep
each theme's own styling and wrappers.

- [ ] **Step 2: Render each theme's post page through goblog**

Use the harness from Task 3 with the theme's templates layered on default's,
as `blog/blog_test.go` does with `../themes/<name>/templates/*.html`, or run
the local server with `theme` set to that theme and fetch a post.

- [ ] **Step 3: Bump each theme's manifest and changelog**

In `goblog-theme.json`, set `min_goblog_version` to the goblog release that
carries this change (the version tagged after Task 7's PR merges). Add a
CHANGELOG entry describing server-side rendering.

- [ ] **Step 4: Open a PR per repo**

Each PR body should say it requires that goblog version and why (a theme
calling `.post.HTML` on an older goblog renders nothing).

---

## Self-review

**Spec coverage:** goldmark instances and the three methods → Task 1; preview endpoint → Task 2; post page and comments → Task 3; custom pages → Task 4; editor tabs → Task 5; `writing-a-theme` docs including the security note → Task 6; real-content verification → Task 7; the two theme repos → Task 8. `.post.Content` staying available is a constraint, satisfied by not removing the field.

**Type consistency:** `renderMarkdown`/`renderComment` (Task 1) are used by `Post.HTML`, `Page.HTML`, `Comment.HTML`; Task 2's handler calls `blog.Post{...}.HTML()`; Tasks 3-5 use `.post.HTML`, `.page.HTML`, `.HTML` and `setupEditorTabs("content")` consistently with the markup ids `content`, `content-write`, `content-preview`.

**Known risk:** the editor templates each construct `simplemde` as a local `var` inside an inline script; `setupEditorTabs` reads it via `typeof simplemde !== 'undefined'`, which works because those are function-scope-free top-level `var`s in a `<script>` block. If a template is later changed to wrap its script in an IIFE or a module, pass the value getter into `setupEditorTabs` instead.
