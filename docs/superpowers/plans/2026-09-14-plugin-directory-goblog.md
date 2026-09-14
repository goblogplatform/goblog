# Plugin Directory (goblog side: pieces A and C) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give goblog a `validate-plugin` subcommand (used by the registry's CI) and a compiled-in `directory` plugin that renders the plugin directory at `/plugins`, `/plugins/<name>` and `/plugins/index.json` from an index built elsewhere.

**Architecture:** `plugin.Validate` wraps the existing Yaegi loader and `main` dispatches `validate-plugin` before any `.env`/DB work. The plugin system gains two small, general extensions — `HookContext.SubPath` (plugin pages can own sub-paths) and "handled by writing the response" (plugin pages can serve raw JSON). `plugins/directory` is a normal compiled-in plugin: a `Fetcher` keeps the last good copy of the index and per-plugin detail JSON in memory (refreshed by a scheduled job, kept on failure), and `RenderPage` renders embedded `html/template` templates into `page_content.html` the way `plugins/scholar` does.

**Tech Stack:** Go 1.25, gin, gorm (sqlite in tests), Yaegi, `embed` + `html/template`, `net/http/httptest` for fixtures. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-09-14-plugin-directory-design.md` (pieces A and C; piece B — the registry and seed plugin repos — is a separate plan).

## Global Constraints

- Module path is `goblog` (imports are `goblog/plugin`, `goblog/blog`, …). Never add `github.com/goblogplatform/goblog` imports.
- No new dependencies in `go.mod`.
- Every string that comes from the index is untrusted: it must pass through `html/template` escaping; only fields ending in `_html` (pre-rendered and sanitized by GitHub at build time) are inserted as `template.HTML`.
- The `directory` plugin is **disabled by default** (`enabled` setting default `"false"`).
- Default `index_url`: `https://goblogplatform.github.io/plugins/index.json`. Default `refresh_minutes`: `15`.
- `index.json` is re-served **verbatim** (the cached bytes), `Content-Type: application/json`, `Cache-Control: public, max-age=300`; `503` JSON error when nothing is cached yet.
- The directory page row: `PageType: "plugin-directory"`, `Slug: "plugins"`, `Title: "Plugins"`, `ShowInNav: true`, `NavOrder: 30`, `Enabled: true`.
- Test/verify commands are the ones CI runs: `go build -v .`, `go vet ./...`, `go test -race goblog/...`.
- Commit messages follow the repo style (imperative, `(#552)` suffix) and end with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- Work on branch `feat/552-plugin-directory`; never push to `main`; never merge. Run `git branch --show-current` before every commit.

---

## File structure

| File | Responsibility |
|---|---|
| `plugin/validate.go` (new) | `Validate(path) (Info, error)` — load one dynamic plugin file and report identity |
| `plugin/validate_test.go` (new) | tests for the above |
| `cmd_validate.go` (new, package main) | `runValidatePlugin(args, stdout, stderr) int` — CLI wrapper |
| `cmd_validate_test.go` (new) | tests for the CLI wrapper |
| `goblog.go` | dispatch `validate-plugin` at top of `main()`; register `directory.New()` |
| `plugin/plugin.go` | `HookContext.SubPath` |
| `plugin/registry.go` | `RenderPluginPage(c, pageType, subPath)`; handled-by-writing |
| `plugin/registry_test.go` | tests for sub-paths / raw responses |
| `blog/blog.go` | `NoRoute` resolves `/<plugin-page-slug>/<sub-path>`; `DynamicPage(c, page, subPath)`; 404 for a declined sub-path |
| `blog/blog_test.go` | `TestPluginPageSubPaths` |
| `plugins/directory/index.go` (new) | `Entry`, `Release`, `Detail` types (index JSON shape) |
| `plugins/directory/fetcher.go` (new) | `Fetcher` — cached HTTP client for index + details |
| `plugins/directory/fetcher_test.go` (new) | httptest-based tests |
| `plugins/directory/directory.go` (new) | the plugin: identity, settings, page, OnInit, scheduled job, RenderPage |
| `plugins/directory/templates/listing.html`, `detail.html` (new) | embedded `html/template` templates |
| `plugins/directory/directory_test.go` (new) | OnInit, job, RenderPage tests |
| `README.md` | document `validate-plugin`, sub-paths/raw responses, the `directory` plugin |

---

### Task 1: `plugin.Validate`

**Files:**
- Create: `plugin/validate.go`
- Test: `plugin/validate_test.go`

**Interfaces:**
- Consumes: `loadPlugin(path string) (Plugin, error)` in `plugin/loader.go` (unexported, same package).
- Produces: `plugin.Info{Name, DisplayName, Version string}` (JSON tags `name`, `display_name`, `version`) and `plugin.Validate(path string) (Info, error)`. Task 2 uses both.

- [ ] **Step 1: Write the failing tests**

`plugin/validate_test.go`:

```go
package plugin_test

import (
	"goblog/plugin"
	"os"
	"path/filepath"
	"testing"
)

// TestValidate_ShippedExample checks the example dynamic plugin validates
// and reports the identity the registry CI will compare against a manifest.
func TestValidate_ShippedExample(t *testing.T) {
	info, err := plugin.Validate("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatalf("validate shipped example: %v", err)
	}
	if info.Name != "hello" || info.DisplayName != "Hello (example)" || info.Version != "1.0.0" {
		t.Errorf("unexpected info: %+v", info)
	}
}

// TestValidate_Errors covers the ways a submitted file can be unusable.
func TestValidate_Errors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := map[string]string{
		"does not compile":    write("broken.go", "package main\nfunc NewPlugin() plugin.Plugin { return nil "),
		"no NewPlugin":        write("noctor.go", "package main\nfunc Other() int { return 1 }\n"),
		"empty Name()":        write("noname.go", "package main\nimport \"goblog/plugin\"\ntype P struct{ plugin.BasePlugin }\nfunc NewPlugin() plugin.Plugin { return &P{} }\nfunc (P) Name() string { return \"\" }\nfunc (P) DisplayName() string { return \"x\" }\nfunc (P) Version() string { return \"1\" }\n"),
		"empty Version()":     write("noversion.go", "package main\nimport \"goblog/plugin\"\ntype P struct{ plugin.BasePlugin }\nfunc NewPlugin() plugin.Plugin { return &P{} }\nfunc (P) Name() string { return \"p\" }\nfunc (P) DisplayName() string { return \"x\" }\nfunc (P) Version() string { return \"\" }\n"),
		"missing file":        filepath.Join(dir, "missing.go"),
	}
	for name, path := range cases {
		if _, err := plugin.Validate(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugin/ -run 'TestValidate' -v`
Expected: FAIL to compile — `undefined: plugin.Validate`.

- [ ] **Step 3: Implement `Validate`**

`plugin/validate.go`:

```go
package plugin

import "fmt"

// Info is what `goblog validate-plugin` reports about a dynamic plugin file.
type Info struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
}

// Validate loads a single dynamic plugin file through the Yaegi loader,
// exactly as LoadDynamicPlugins does at startup, and reports its identity.
// The plugin registry's CI runs it (via `goblog validate-plugin`) to check a
// submitted plugin loads and that its Name and Version match the manifest
// and release tag.
func Validate(path string) (Info, error) {
	p, err := loadPlugin(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{Name: p.Name(), DisplayName: p.DisplayName(), Version: p.Version()}
	if info.Name == "" {
		return Info{}, fmt.Errorf("%s: Name() returned an empty string", path)
	}
	if info.Version == "" {
		return Info{}, fmt.Errorf("%s: Version() returned an empty string", path)
	}
	return info, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./plugin/ -run 'TestValidate' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git branch --show-current   # must be feat/552-plugin-directory
git add plugin/validate.go plugin/validate_test.go
git commit -m "Add plugin.Validate to load and identify a single dynamic plugin file (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `goblog validate-plugin` subcommand

**Files:**
- Create: `cmd_validate.go`, `cmd_validate_test.go` (package `main`)
- Modify: `goblog.go:203-205` (top of `main()`)
- Modify: `README.md` "Dynamic plugins" section (after the Docker bind-mount block, before `## Testing`)

**Interfaces:**
- Consumes: `gplugin.Validate`, `gplugin.Info` from Task 1.
- Produces: `runValidatePlugin(args []string, stdout, stderr io.Writer) int` (exit code: 0 ok, 1 invalid plugin, 2 usage).

- [ ] **Step 1: Write the failing test**

`cmd_validate_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunValidatePlugin(t *testing.T) {
	var out, errOut bytes.Buffer

	// Happy path: JSON identity on stdout, exit 0.
	if code := runValidatePlugin([]string{"plugins/dynamic/hello.go.example"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	var info map[string]string
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("stdout is not JSON: %v: %q", err, out.String())
	}
	if info["name"] != "hello" || info["display_name"] != "Hello (example)" || info["version"] != "1.0.0" {
		t.Errorf("unexpected identity: %v", info)
	}

	// A file that does not load: message on stderr, exit 1, nothing on stdout.
	out.Reset()
	errOut.Reset()
	if code := runValidatePlugin([]string{"does-not-exist.go"}, &out, &errOut); code != 1 {
		t.Errorf("expected exit 1 for a missing file, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid plugin") || out.Len() != 0 {
		t.Errorf("expected error on stderr only, stdout=%q stderr=%q", out.String(), errOut.String())
	}

	// Usage error: exit 2.
	errOut.Reset()
	if code := runValidatePlugin(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Errorf("expected usage error and exit 2, got %d %q", code, errOut.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test . -run TestRunValidatePlugin -v`
Expected: FAIL to compile — `undefined: runValidatePlugin`.

- [ ] **Step 3: Implement the wrapper and dispatch it from `main`**

`cmd_validate.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"

	gplugin "goblog/plugin"
)

// runValidatePlugin implements `goblog validate-plugin <file.go>`: it loads
// the file through the dynamic plugin loader and prints the plugin's identity
// as JSON. Returns the process exit code. The plugin registry's CI runs this
// against the release Docker image to check a submission before listing it.
func runValidatePlugin(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: goblog validate-plugin <file.go>")
		return 2
	}
	info, err := gplugin.Validate(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "invalid plugin: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(info); err != nil {
		fmt.Fprintf(stderr, "write result: %v\n", err)
		return 1
	}
	return 0
}
```

In `goblog.go`, make these the first lines of `main()` — before the version log line and before the `.env` bootstrap, which would otherwise create a `.env` in the caller's working directory:

```go
func main() {
	// Subcommands run and exit before any .env or database work.
	if len(os.Args) > 1 && os.Args[1] == "validate-plugin" {
		os.Exit(runValidatePlugin(os.Args[2:], os.Stdout, os.Stderr))
	}
	log.Println("Starting blog version: ", Version)
```

- [ ] **Step 4: Run the test and a manual check**

Run: `go test . -run TestRunValidatePlugin -v`
Expected: PASS.

Run: `go build -o goblog . && ./goblog validate-plugin plugins/dynamic/hello.go.example; echo "exit=$?"; ls .env 2>/dev/null`
Expected: `{"name":"hello","display_name":"Hello (example)","version":"1.0.0"}`, `exit=0`, and **no** `.env` listed (unless one already existed before you ran this — check with `git status`; `.env` is gitignored so it would not show, so run the command in a temp dir copy of the binary if in doubt: `cd $(mktemp -d) && /path/to/goblog validate-plugin /path/to/goblog/plugins/dynamic/hello.go.example && ls -a`).

- [ ] **Step 5: Document it in the README**

Insert after the Docker bind-mount code block in "### Dynamic plugins" (just before `## Testing`):

````markdown
#### Checking a plugin file
`goblog validate-plugin <file.go>` loads a single file through the same interpreter and prints its identity as JSON (exit 1 with the load error on stderr if it fails):
```bash
./goblog validate-plugin plugins/dynamic/hello.go.example
# {"name":"hello","display_name":"Hello (example)","version":"1.0.0"}
```
With the Docker image (its entrypoint is a shell command, so override it):
```bash
docker run --rm -v "$PWD:/p" --entrypoint /go/src/github.com/compscidr/goblog/goblog \
  compscidr/goblog:latest validate-plugin /p/plugin.go
```
This is what the [plugin directory](https://goblog.live/plugins) registry runs on every submission.
````

- [ ] **Step 6: Commit**

```bash
git branch --show-current
git add cmd_validate.go cmd_validate_test.go goblog.go README.md
git commit -m "Add goblog validate-plugin subcommand for the plugin registry CI (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Plugin pages: sub-paths and raw responses (plugin package)

**Files:**
- Modify: `plugin/plugin.go` (`HookContext`, lines ~33-40)
- Modify: `plugin/registry.go:217-235` (`RenderPluginPage`)
- Test: `plugin/registry_test.go`

**Interfaces:**
- Produces: `HookContext.SubPath string`; `(*Registry).RenderPluginPage(c *gin.Context, pageType, subPath string) (tmpl string, data gin.H, handled bool)` where `handled && tmpl == ""` means the plugin already wrote the response. Task 4 and Task 7 depend on these.

- [ ] **Step 1: Write the failing test**

Append to `plugin/registry_test.go` (it already imports `net/http`, `net/http/httptest`, `gin`, sqlite, gorm):

```go
// pagePlugin owns a page and uses SubPath: "" renders a template, "data.json"
// writes JSON itself, anything else is declined.
type pagePlugin struct {
	plugin.BasePlugin
}

func (p *pagePlugin) Name() string        { return "pager" }
func (p *pagePlugin) DisplayName() string { return "Pager" }
func (p *pagePlugin) Version() string     { return "1.0.0" }
func (p *pagePlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{{Key: "enabled", Type: "text", DefaultValue: "true", Label: "Enabled"}}
}
func (p *pagePlugin) Pages() []plugin.PageDefinition {
	return []plugin.PageDefinition{{PageType: "pager", Title: "Pager", Slug: "pager"}}
}
func (p *pagePlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	switch ctx.SubPath {
	case "":
		return "page_content.html", gin.H{"plugin_content": "root"}
	case "data.json":
		ctx.GinContext.JSON(http.StatusOK, gin.H{"ok": true})
		return "", nil
	}
	return "", nil
}

func TestRenderPluginPage_SubPathsAndRawResponses(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	newCtx := func(path string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		return c, w
	}

	// The page itself: template + data, SubPath is empty.
	c, _ := newCtx("/pager")
	tmpl, data, handled := reg.RenderPluginPage(c, "pager", "")
	if !handled || tmpl != "page_content.html" || data["plugin_content"] != "root" {
		t.Errorf("root: handled=%v tmpl=%q data=%v", handled, tmpl, data)
	}

	// A sub-path the plugin answers by writing the response: handled, no template.
	c, w := newCtx("/pager/data.json")
	tmpl, _, handled = reg.RenderPluginPage(c, "pager", "data.json")
	if !handled || tmpl != "" {
		t.Errorf("data.json: handled=%v tmpl=%q", handled, tmpl)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("data.json: body=%q content-type=%q", w.Body.String(), w.Header().Get("Content-Type"))
	}

	// A sub-path the plugin declines: not handled, nothing written.
	c, w = newCtx("/pager/nope")
	if _, _, handled = reg.RenderPluginPage(c, "pager", "nope"); handled || w.Body.Len() != 0 {
		t.Errorf("nope: handled=%v body=%q", handled, w.Body.String())
	}

	// Disabled plugin: never called.
	reg.UpdateSetting("pager", "enabled", "false")
	c, _ = newCtx("/pager")
	if _, _, handled = reg.RenderPluginPage(c, "pager", ""); handled {
		t.Error("disabled plugin should not handle its page")
	}
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./plugin/ -run TestRenderPluginPage_SubPathsAndRawResponses -v`
Expected: FAIL to compile — `ctx.SubPath undefined` and `too many arguments in call to reg.RenderPluginPage`.

- [ ] **Step 3: Implement**

`plugin/plugin.go` — add to `HookContext` after `Data`:

```go
	// SubPath is set for RenderPage only: the request path after the page's
	// slug, without the leading slash. "" for /plugins, "hello" for
	// /plugins/hello, "index.json" for /plugins/index.json. Plugins that
	// don't use it can ignore it: blog only routes sub-paths to plugin pages.
	SubPath string
```

`plugin/registry.go` — replace `RenderPluginPage`:

```go
// RenderPluginPage renders a plugin-owned page. subPath is the request path
// after the page slug ("" for the page itself). Returns the template name,
// its data, and whether the request was handled. A plugin handles a request
// either by returning a template name or by writing the response itself
// (for example JSON), in which case the template name is empty and the
// caller must not render anything.
func (r *Registry) RenderPluginPage(c *gin.Context, pageType, subPath string) (string, gin.H, bool) {
	p := r.GetPagePlugin(pageType)
	if p == nil {
		return "", nil, false
	}
	settings := r.getPluginSettings(p.Name())
	ctx := &HookContext{
		GinContext: c,
		DB:         r.db,
		Settings:   settings,
		Template:   pageType,
		SubPath:    subPath,
	}
	tmpl, data := p.RenderPage(ctx, pageType)
	if tmpl == "" {
		return "", nil, c.Writer.Written()
	}
	return tmpl, data, true
}
```

- [ ] **Step 4: Run the plugin package tests**

Run: `go test ./plugin/ -v`
Expected: PASS for all. (`go build ./...` will fail in `blog` until Task 4 — that is expected; do not touch `blog` here.)

Run: `go vet ./plugin/`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git branch --show-current
git add plugin/plugin.go plugin/registry.go plugin/registry_test.go
git commit -m "Let plugin pages receive sub-paths and write raw responses (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Route `/<plugin-page>/<sub-path>` in blog

**Files:**
- Modify: `blog/blog.go` — `DynamicPage` (line ~505), its `default:` branch (~559-600), `NoRoute` tail (~757-786)
- Test: `blog/blog_test.go`

**Interfaces:**
- Consumes: `RenderPluginPage(c, pageType, subPath)` and the registry's `HasPageType`, `IsPageTypeEnabled` (already exist).
- Produces: `(*Blog).DynamicPage(c *gin.Context, page *Page, subPath string)`; unexported `pluginRegistry` interface and `pluginRegistryFrom(c)`, `renderNotFound(c)` helpers.

- [ ] **Step 1: Write the failing test**

Append to `blog/blog_test.go` (existing imports cover `html/template`, `net/http`, `net/http/httptest`, gin, sqlite, gorm, mock; add `goblog/plugin` and `strings` if not already imported):

```go
// subPathPlugin owns the "dir" page and answers "" (listing) and "index.json"
// (raw JSON); everything else is declined.
type subPathPlugin struct {
	plugin.BasePlugin
}

func (p *subPathPlugin) Name() string        { return "dir" }
func (p *subPathPlugin) DisplayName() string { return "Dir" }
func (p *subPathPlugin) Version() string     { return "1.0.0" }
func (p *subPathPlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{{Key: "enabled", Type: "text", DefaultValue: "true", Label: "Enabled"}}
}
func (p *subPathPlugin) Pages() []plugin.PageDefinition {
	return []plugin.PageDefinition{{PageType: "dir", Title: "Dir", Slug: "dir"}}
}
func (p *subPathPlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	switch ctx.SubPath {
	case "":
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": "<p>LISTING</p>"}
	case "index.json":
		ctx.GinContext.Data(http.StatusOK, "application/json", []byte(`[{"name":"x"}]`))
		return "", nil
	}
	return "", nil
}

// TestPluginPageSubPaths covers issue #552: a plugin-owned page also owns
// everything under its slug, while built-in pages keep single-segment slugs.
func TestPluginPageSubPaths(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", Enabled: true})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, Enabled: true, Content: "about"})

	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")

	reg := plugin.NewRegistry(db)
	reg.Register(&subPathPlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.NoRoute(b.NoRoute)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		return w
	}

	if w := get("/dir"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "LISTING") {
		t.Errorf("/dir: code=%d body has LISTING=%v", w.Code, strings.Contains(w.Body.String(), "LISTING"))
	}
	if w := get("/dir/index.json"); w.Code != http.StatusOK || w.Body.String() != `[{"name":"x"}]` || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/dir/index.json: code=%d body=%q type=%q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := get("/dir/nope"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "404") {
		t.Errorf("/dir/nope: expected a 404 page, got code=%d", w.Code)
	}
	// Built-in pages do not get sub-paths.
	if w := get("/about"); w.Code != http.StatusOK {
		t.Errorf("/about: code=%d", w.Code)
	}
	if w := get("/about/anything"); w.Code != http.StatusNotFound {
		t.Errorf("/about/anything: expected 404, got %d", w.Code)
	}
	// A disabled plugin's page and sub-paths show the "not available" page.
	reg.UpdateSetting("dir", "enabled", "false")
	if w := get("/dir/index.json"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "currently disabled") {
		t.Errorf("/dir/index.json disabled: code=%d", w.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./blog/ -run TestPluginPageSubPaths -v`
Expected: FAIL to compile — the local `pluginPageHandler` interface in `DynamicPage` no longer matches `RenderPluginPage`'s signature (`go build ./...` reports it under `blog/blog.go`).

- [ ] **Step 3: Implement in `blog/blog.go`**

(a) Add near `PageFilter` (top of file):

```go
// pluginRegistry is the part of the plugin registry the blog needs to
// resolve and render plugin-owned pages. It is looked up from the Gin
// context (set by plugin.Middleware) so blog does not import plugin.
type pluginRegistry interface {
	RenderPluginPage(c *gin.Context, pageType, subPath string) (string, gin.H, bool)
	HasPageType(pageType string) bool
	IsPageTypeEnabled(pageType string) bool
}

func pluginRegistryFrom(c *gin.Context) pluginRegistry {
	if reg, exists := c.Get("plugin_registry"); exists {
		if r, ok := reg.(pluginRegistry); ok {
			return r
		}
	}
	return nil
}
```

(b) Change the `DynamicPage` signature and its `default:` branch. Signature:

```go
// DynamicPage renders the appropriate template for a page based on its PageType.
// subPath is the request path after the page's slug; it is only ever non-empty
// for plugin-owned pages (see NoRoute) and is passed through to the plugin.
func (b *Blog) DynamicPage(c *gin.Context, page *Page, subPath string) {
```

Replace the whole `default:` branch up to (not including) the `// Fallback: render as custom content page` comment with:

```go
	default:
		// Check if a plugin handles this page type
		if r := pluginRegistryFrom(c); r != nil {
			tmpl, pluginData, handled := r.RenderPluginPage(c, page.PageType, subPath)
			if handled {
				if tmpl == "" {
					return // the plugin wrote the response itself (e.g. JSON)
				}
				data := gin.H{
					"logged_in":  b.auth.IsLoggedIn(c),
					"is_admin":   b.auth.IsAdmin(c),
					"page":       page,
					"version":    b.Version,
					"title":      page.Title,
					"recent":     b.GetLatest(),
					"admin_page": false,
					"settings":   b.GetSettings(),
					"nav_pages":  navPages,
				}
				for k, v := range pluginData {
					data[k] = v
				}
				b.Render(c, http.StatusOK, tmpl, data)
				return
			}
			if r.HasPageType(page.PageType) {
				if r.IsPageTypeEnabled(page.PageType) {
					// The plugin is on but declined the request (an unknown sub-path).
					b.renderNotFound(c)
					return
				}
				// Plugin owns this page type but is disabled — show 404
				b.Render(c, http.StatusNotFound, "error.html", gin.H{
					"error":       "Page Not Available",
					"description": "This page is currently disabled.",
					"version":     b.Version,
					"title":       "Not Available",
					"recent":      b.GetLatest(),
					"admin_page":  false,
					"settings":    b.GetSettings(),
					"nav_pages":   navPages,
				})
				return
			}
		}
```

(c) In `NoRoute`, replace the block from `// Try to resolve as a dynamic page or post type listing by slug` to the end of the function with:

```go
	// Try to resolve as a dynamic page or post type listing by slug. Only
	// plugin-owned pages own what is under their slug (/plugins/hello,
	// /plugins/index.json); built-in pages and post types are single-segment.
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	path = strings.TrimSuffix(path, "/")
	slug, subPath, _ := strings.Cut(path, "/")
	if slug != "" {
		// Check dynamic page first (pages have hero content, edit links, etc.)
		page, err := b.GetPageBySlug(slug)
		if err == nil && page != nil {
			if subPath == "" {
				b.DynamicPage(c, page, "")
				return
			}
			if r := pluginRegistryFrom(c); r != nil && r.HasPageType(page.PageType) {
				b.DynamicPage(c, page, subPath)
				return
			}
		}

		// Then try post type listing
		if subPath == "" {
			pt, err := b.GetPostTypeBySlug(slug)
			if err == nil && pt != nil {
				b.PostTypeListing(c, pt)
				return
			}
		}
	}

	b.renderNotFound(c)
}

// renderNotFound renders the generic 404 page.
func (b *Blog) renderNotFound(c *gin.Context) {
	b.Render(c, http.StatusNotFound, "error.html", gin.H{
		"logged_in":   b.auth.IsLoggedIn(c),
		"is_admin":    b.auth.IsAdmin(c),
		"error":       "404: Page Not Found",
		"description": "The page at '" + c.Request.URL.String() + "' was not found",
		"version":     b.Version,
		"recent":      b.GetLatest(),
		"admin_page":  false,
		"settings":    b.GetSettings(),
		"nav_pages":   b.GetNavPages(),
	})
}
```

(The existing 404 map at the end of `NoRoute` moves into `renderNotFound` unchanged.)

- [ ] **Step 4: Build and run tests**

Run: `go build ./... && go vet ./... && go test -race ./blog/ ./plugin/ ./plugins/...`
Expected: build clean, vet silent, all PASS (including the pre-existing `TestBlogWorkflow`, which exercises `/about`, `/tags`, `/archives` through `NoRoute`).

- [ ] **Step 5: Document in the README plugin table**

In `README.md`, replace the `Pages()` / `RenderPage` row of the hook table with:

```markdown
| `Pages()` / `RenderPage(ctx, pageType)` | Own a page type: it gets a slug, an optional nav entry, and you choose the template and data when it is visited. The plugin also owns everything under its slug: `ctx.SubPath` is `""` for `/research`, `"2024"` for `/research/2024`. Return a template name to render it inside the theme, or write the response yourself (e.g. `ctx.GinContext.JSON(...)`) and return `""`; returning `""` without writing anything gives a 404. `plugins/scholar` is the simplest example, `plugins/directory` uses sub-paths. |
```

- [ ] **Step 6: Commit**

```bash
git branch --show-current
git add blog/blog.go blog/blog_test.go README.md
git commit -m "Route sub-paths under plugin-owned pages to the plugin (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Directory index types and `Fetcher`

**Files:**
- Create: `plugins/directory/index.go`, `plugins/directory/fetcher.go`
- Test: `plugins/directory/fetcher_test.go`

**Interfaces:**
- Produces (all in package `directory`):
  - `type Entry struct` (index.json entry), `type Release struct`, `type Detail struct { Entry; ReadmeHTML, ChangelogHTML template.HTML; Releases []Release }`.
  - `NewFetcher(client *http.Client) *Fetcher`
  - `(*Fetcher).Refresh(indexURL string) error` — fetch + parse; keeps the previous copy on any error; re-fetches already-cached details.
  - `(*Fetcher).Ensure(indexURL string)` — `Refresh` only if nothing is cached yet.
  - `(*Fetcher).Index() (raw []byte, entries []Entry, ok bool)`
  - `(*Fetcher).Entry(name string) (Entry, bool)`
  - `(*Fetcher).Detail(name string) (*Detail, error)` — lazy fetch of `entry.DetailURL`, cached.
  - `(*Fetcher).FetchedAt() time.Time`
  - Task 6/7 use all of these.

- [ ] **Step 1: Write the failing tests**

`plugins/directory/fetcher_test.go`:

```go
package directory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const fixtureIndex = `[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason","license":"GPL-3.0","source_url":"https://github.com/goblogplatform/goblog-plugin-hello","download_url":"https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go","sha256":"abc","min_goblog_version":"0.2.6","install_type":"dynamic","released_at":"2026-09-14T00:00:00Z","detail_url":"DETAIL_URL"}]`

const fixtureDetail = `{"name":"hello","display_name":"Hello","version":"1.0.0","readme_html":"<h1>Hello</h1>","changelog_html":"","releases":[{"version":"1.0.0","released_at":"2026-09-14T00:00:00Z","notes_html":"<p>First</p>","url":"https://github.com/goblogplatform/goblog-plugin-hello/releases/tag/v1.0.0"}]}`

// fixtureServer serves index.json and plugins/hello.json; the handlers can be
// swapped to simulate failures.
type fixtureServer struct {
	*httptest.Server
	index  atomic.Value // func(w http.ResponseWriter)
	detail atomic.Value
	hits   atomic.Int32
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	fs.index.Store(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.ReplaceAll(fixtureIndex, "DETAIL_URL", fs.URL+"/plugins/hello.json")))
	})
	fs.detail.Store(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixtureDetail))
	})
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		switch r.URL.Path {
		case "/index.json":
			fs.index.Load().(func(http.ResponseWriter))(w)
		case "/plugins/hello.json":
			fs.detail.Load().(func(http.ResponseWriter))(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

func TestFetcher_RefreshAndLookup(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())

	if _, _, ok := f.Index(); ok {
		t.Fatal("expected nothing cached before the first refresh")
	}
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	raw, entries, ok := f.Index()
	if !ok || len(entries) != 1 || entries[0].Name != "hello" || entries[0].Version != "1.0.0" {
		t.Fatalf("index: ok=%v entries=%+v", ok, entries)
	}
	if !strings.Contains(string(raw), `"name":"hello"`) {
		t.Errorf("raw bytes should be the served index verbatim, got %q", raw)
	}
	if e, ok := f.Entry("hello"); !ok || e.DisplayName != "Hello" {
		t.Errorf("Entry(hello): ok=%v e=%+v", ok, e)
	}
	if _, ok := f.Entry("nope"); ok {
		t.Error("Entry(nope) should not be found")
	}
	if f.FetchedAt().IsZero() {
		t.Error("FetchedAt should be set after a refresh")
	}

	d, err := f.Detail("hello")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.ReadmeHTML != "<h1>Hello</h1>" || len(d.Releases) != 1 || d.Releases[0].NotesHTML != "<p>First</p>" {
		t.Errorf("detail: %+v", d)
	}
	// Detail is cached: a second call does not hit the server.
	before := srv.hits.Load()
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != before {
		t.Error("second Detail call should be served from cache")
	}
	if _, err := f.Detail("nope"); err == nil {
		t.Error("Detail(nope) should fail")
	}
}

func TestFetcher_KeepsLastGoodCopyOnFailure(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}

	// HTTP 500: error returned, old index and details still served.
	srv.index.Store(func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	if err := f.Refresh(srv.URL + "/index.json"); err == nil {
		t.Error("expected an error on HTTP 500")
	}
	if _, entries, ok := f.Index(); !ok || len(entries) != 1 {
		t.Errorf("index should be kept after a failed refresh: ok=%v n=%d", ok, len(entries))
	}
	if d, err := f.Detail("hello"); err != nil || d.ReadmeHTML == "" {
		t.Errorf("detail should be kept after a failed refresh: %v", err)
	}

	// Malformed JSON: same.
	srv.index.Store(func(w http.ResponseWriter) { w.Write([]byte(`{not json`)) })
	if err := f.Refresh(srv.URL + "/index.json"); err == nil {
		t.Error("expected an error on malformed JSON")
	}
	if _, entries, ok := f.Index(); !ok || len(entries) != 1 {
		t.Error("index should be kept after malformed JSON")
	}

	// Unreachable host: same.
	if err := f.Refresh("http://127.0.0.1:1/index.json"); err == nil {
		t.Error("expected an error for an unreachable host")
	}
	if _, _, ok := f.Index(); !ok {
		t.Error("index should be kept after a connection error")
	}
}

func TestFetcher_RefreshUpdatesCachedDetails(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}
	srv.detail.Store(func(w http.ResponseWriter) {
		w.Write([]byte(strings.Replace(fixtureDetail, "<h1>Hello</h1>", "<h1>Hello v2</h1>", 1)))
	})
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.Detail("hello"); d.ReadmeHTML != "<h1>Hello v2</h1>" {
		t.Errorf("cached detail should be re-fetched on refresh, got %q", d.ReadmeHTML)
	}
}

func TestFetcher_Ensure(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	f.Ensure(srv.URL + "/index.json")
	if _, _, ok := f.Index(); !ok {
		t.Fatal("Ensure should fetch when nothing is cached")
	}
	before := srv.hits.Load()
	f.Ensure(srv.URL + "/index.json")
	if srv.hits.Load() != before {
		t.Error("Ensure should not fetch again when a copy is cached")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/directory/ -v`
Expected: FAIL to compile — `undefined: NewFetcher`.

- [ ] **Step 3: Implement the types and the fetcher**

`plugins/directory/index.go`:

```go
package directory

import "html/template"

// Entry is one plugin in index.json: the latest release of a plugin as
// published by the registry (github.com/goblogplatform/plugins).
type Entry struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Version          string `json:"version"`
	Author           string `json:"author"`
	License          string `json:"license"`
	SourceURL        string `json:"source_url"`
	DownloadURL      string `json:"download_url"`
	SHA256           string `json:"sha256"`
	MinGoblogVersion string `json:"min_goblog_version"`
	InstallType      string `json:"install_type"`
	ReleasedAt       string `json:"released_at"` // RFC 3339; shown as-is, never parsed
	DetailURL        string `json:"detail_url"`
}

// Release is one entry of a plugin's release history.
type Release struct {
	Version    string        `json:"version"`
	ReleasedAt string        `json:"released_at"`
	NotesHTML  template.HTML `json:"notes_html"` // rendered and sanitized by the registry build
	URL        string        `json:"url"`
}

// Detail is plugins/<name>.json: the index entry plus the README, changelog
// and release history. The *_html fields are rendered through GitHub's
// markdown API by the registry build, which sanitizes them; they are the
// only fields inserted into pages unescaped.
type Detail struct {
	Entry
	ReadmeHTML    template.HTML `json:"readme_html"`
	ChangelogHTML template.HTML `json:"changelog_html"`
	Releases      []Release     `json:"releases"`
}
```

`plugins/directory/fetcher.go`:

```go
package directory

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// maxIndexBytes caps what is read from the registry so a misbehaving index
// URL cannot exhaust memory.
const maxIndexBytes = 8 << 20

// Fetcher keeps an in-memory copy of the directory index and of the detail
// JSON for plugins that have been viewed. Every fetch that fails keeps the
// previous copy, so the directory degrades to "slightly stale" rather than
// "empty" when the registry is unreachable.
type Fetcher struct {
	client    *http.Client
	userAgent string

	mu        sync.RWMutex
	raw       []byte           // index.json bytes, served verbatim
	entries   []Entry          // parsed raw
	byName    map[string]Entry // entries keyed by Name
	details   map[string]*Detail
	fetchedAt time.Time
}

// NewFetcher returns a Fetcher using client (nil means a 10s-timeout default).
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Fetcher{client: client, userAgent: "goblog-directory", details: map[string]*Detail{}}
}

// SetUserAgent sets the User-Agent sent to the registry.
func (f *Fetcher) SetUserAgent(ua string) { f.userAgent = ua }

// Refresh downloads and parses indexURL, replacing the cached index on
// success and re-fetching the details of every plugin already cached. On any
// error the previous index and details are kept and the error returned.
func (f *Fetcher) Refresh(indexURL string) error {
	raw, err := f.get(indexURL)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("parse index: %w", err)
	}
	byName := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}

	f.mu.Lock()
	f.raw, f.entries, f.byName, f.fetchedAt = raw, entries, byName, time.Now()
	cached := make([]string, 0, len(f.details))
	for name := range f.details {
		cached = append(cached, name)
	}
	f.mu.Unlock()

	// Refresh details we already hold; a failure keeps the old detail.
	for _, name := range cached {
		e, ok := byName[name]
		if !ok {
			f.mu.Lock()
			delete(f.details, name) // plugin left the registry
			f.mu.Unlock()
			continue
		}
		if d, err := f.fetchDetail(e); err == nil {
			f.mu.Lock()
			f.details[name] = d
			f.mu.Unlock()
		}
	}
	return nil
}

// Ensure fetches the index if nothing is cached yet. Errors are logged by
// the caller's next Refresh; here they simply leave the cache empty.
func (f *Fetcher) Ensure(indexURL string) {
	f.mu.RLock()
	empty := f.raw == nil
	f.mu.RUnlock()
	if empty {
		_ = f.Refresh(indexURL)
	}
}

// Index returns the cached index bytes and entries; ok is false when nothing
// has been fetched successfully yet.
func (f *Fetcher) Index() (raw []byte, entries []Entry, ok bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.raw, f.entries, f.raw != nil
}

// Entry looks a plugin up by name in the cached index.
func (f *Fetcher) Entry(name string) (Entry, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	e, ok := f.byName[name]
	return e, ok
}

// FetchedAt is when the index was last fetched successfully (zero if never).
func (f *Fetcher) FetchedAt() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.fetchedAt
}

// Detail returns the detail JSON for a plugin, fetching and caching it on
// first use. It fails when the plugin is not in the index or the fetch fails
// and no cached copy exists.
func (f *Fetcher) Detail(name string) (*Detail, error) {
	f.mu.RLock()
	d, cached := f.details[name]
	e, listed := f.byName[name]
	f.mu.RUnlock()
	if cached {
		return d, nil
	}
	if !listed {
		return nil, fmt.Errorf("plugin %q is not in the index", name)
	}
	d, err := f.fetchDetail(e)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.details[name] = d
	f.mu.Unlock()
	return d, nil
}

func (f *Fetcher) fetchDetail(e Entry) (*Detail, error) {
	raw, err := f.get(e.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("fetch detail for %s: %w", e.Name, err)
	}
	var d Detail
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse detail for %s: %w", e.Name, err)
	}
	return &d, nil
}

func (f *Fetcher) get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./plugins/directory/ -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git branch --show-current
git add plugins/directory/index.go plugins/directory/fetcher.go plugins/directory/fetcher_test.go
git commit -m "Add the plugin directory index types and cached fetcher (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: `directory` plugin: identity, settings, page, OnInit, scheduled job

**Files:**
- Create: `plugins/directory/directory.go`
- Test: `plugins/directory/directory_test.go`

**Interfaces:**
- Consumes: `Fetcher` from Task 5; `blog.Page` (`goblog/blog`), `gplugin.*` (`goblog/plugin`).
- Produces: `directory.New() *Plugin`; `Plugin` implements `gplugin.Plugin`; unexported `indexURL(settings)`, `refreshInterval(settings)`, `PageType = "plugin-directory"`. `RenderPage` is a stub here (returns `"", nil`) and is completed in Task 7.

- [ ] **Step 1: Write the failing tests**

`plugins/directory/directory_test.go`:

```go
package directory

import (
	"goblog/blog"
	gplugin "goblog/plugin"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "directory" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	defaults := map[string]string{}
	for _, s := range p.Settings() {
		defaults[s.Key] = s.DefaultValue
	}
	if defaults["enabled"] != "false" {
		t.Errorf("must be disabled by default, got %q", defaults["enabled"])
	}
	if defaults["index_url"] != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("index_url default: %q", defaults["index_url"])
	}
	if defaults["refresh_minutes"] != "15" {
		t.Errorf("refresh_minutes default: %q", defaults["refresh_minutes"])
	}
	pages := p.Pages()
	if len(pages) != 1 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
	}
	var _ gplugin.Plugin = p
}

func TestSettingsHelpers(t *testing.T) {
	if got := indexURL(map[string]string{}); got != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("indexURL default: %q", got)
	}
	if got := indexURL(map[string]string{"index_url": " https://example.test/i.json "}); got != "https://example.test/i.json" {
		t.Errorf("indexURL trims: %q", got)
	}
	cases := map[string]time.Duration{"": 15 * time.Minute, "abc": 15 * time.Minute, "0": 15 * time.Minute, "-3": 15 * time.Minute, "5": 5 * time.Minute}
	for in, want := range cases {
		if got := refreshInterval(map[string]string{"refresh_minutes": in}); got != want {
			t.Errorf("refreshInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOnInit_CreatesPageOnce(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	var pages []blog.Page
	db.Where("page_type = ?", PageType).Find(&pages)
	if len(pages) != 1 {
		t.Fatalf("expected exactly one directory page, got %d", len(pages))
	}
	pg := pages[0]
	if pg.Slug != "plugins" || pg.Title != "Plugins" || !pg.ShowInNav || pg.NavOrder != 30 || !pg.Enabled {
		t.Errorf("page: %+v", pg)
	}
}

func TestScheduledJob_RefreshesWhenStale(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	jobs := p.ScheduledJobs()
	if len(jobs) != 1 || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs: %+v", jobs)
	}
	settings := map[string]string{"index_url": srv.URL + "/index.json", "refresh_minutes": "15"}

	// Empty cache: the job fetches.
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := p.fetcher.Index(); !ok {
		t.Fatal("job should have fetched the index")
	}
	// Fresh cache: the job is a no-op.
	before := srv.hits.Load()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != before {
		t.Error("job should not refetch a fresh index")
	}
	// Stale cache: the job fetches again.
	p.fetcher.mu.Lock()
	p.fetcher.fetchedAt = time.Now().Add(-16 * time.Minute)
	p.fetcher.mu.Unlock()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() == before {
		t.Error("job should refetch a stale index")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/directory/ -run 'TestPlugin_Identity|TestSettingsHelpers|TestOnInit|TestScheduledJob' -v`
Expected: FAIL to compile — `undefined: New`, `PageType`, `indexURL`, `refreshInterval`.

- [ ] **Step 3: Implement the plugin (RenderPage stubbed)**

`plugins/directory/directory.go`:

```go
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
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./plugins/directory/ -v`
Expected: PASS (all tests from Tasks 5 and 6).

- [ ] **Step 5: Commit**

```bash
git branch --show-current
git add plugins/directory/directory.go plugins/directory/directory_test.go
git commit -m "Add the directory plugin: settings, plugins page and index refresh job (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: `RenderPage`: listing, `index.json`, detail

**Files:**
- Create: `plugins/directory/templates/listing.html`, `plugins/directory/templates/detail.html`, `plugins/directory/render.go`
- Modify: `plugins/directory/directory.go` (replace the `RenderPage` stub)
- Test: `plugins/directory/directory_test.go` (append)

**Interfaces:**
- Consumes: `HookContext.SubPath` and `GinContext` (Task 3), `Fetcher` (Task 5).
- Produces: `RenderPage` behaviour per spec. Unexported: `templates` (`*template.Template` parsed from `embed.FS`), `renderListing(base string, entries []Entry) (string, error)`, `renderDetail(base string, d *Detail) (string, error)`, `basePath(c *gin.Context) string`, `validName(name string) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `plugins/directory/directory_test.go` (add imports `net/http`, `net/http/httptest`, `strings`, `github.com/gin-gonic/gin`):

```go
func newRenderCtx(t *testing.T, path, subPath string, settings map[string]string) (*gplugin.HookContext, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return &gplugin.HookContext{GinContext: c, Settings: settings, SubPath: subPath}, w
}

func TestRenderPage_Listing(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	// Wrong page type: declined.
	ctx, _ := newRenderCtx(t, "/plugins", "", settings)
	if tmpl, _ := p.RenderPage(ctx, "other"); tmpl != "" {
		t.Errorf("expected other page types to be declined, got %q", tmpl)
	}

	// Listing: fetched on first request, rendered into page_content.html.
	ctx, _ = newRenderCtx(t, "/plugins", "", settings)
	tmpl, data := p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || data["has_plugin_content"] != true {
		t.Fatalf("tmpl=%q data=%v", tmpl, data)
	}
	html, _ := data["plugin_content"].(string)
	for _, want := range []string{`href="/plugins/hello"`, "Hello", "Says hi", "1.0.0", "Jason", "GPL-3.0", "dynamic", `href="/plugins/index.json"`, `href="https://github.com/goblogplatform/goblog-plugin-hello"`} {
		if !strings.Contains(html, want) {
			t.Errorf("listing missing %q in:\n%s", want, html)
		}
	}
}

func TestRenderPage_ListingEscapesIndexStrings(t *testing.T) {
	srv := newFixtureServer(t)
	srv.index.Store(func(w http.ResponseWriter) {
		w.Write([]byte(`[{"name":"evil","display_name":"<script>alert(1)</script>","description":"x","version":"1","source_url":"javascript:alert(1)","install_type":"dynamic"}]`))
	})
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins", "", map[string]string{"index_url": srv.URL + "/index.json"})
	_, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if strings.Contains(html, "<script>") {
		t.Errorf("display_name must be escaped:\n%s", html)
	}
	if strings.Contains(html, `href="javascript:`) {
		t.Errorf("unsafe source_url must be neutralised:\n%s", html)
	}
}

func TestRenderPage_ListingUnavailable(t *testing.T) {
	p := New()
	p.fetcher = NewFetcher(&http.Client{Timeout: time.Second})
	ctx, _ := newRenderCtx(t, "/plugins", "", map[string]string{"index_url": "http://127.0.0.1:1/index.json"})
	tmpl, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if tmpl != "page_content.html" || !strings.Contains(html, "unavailable") {
		t.Errorf("expected an unavailable message, tmpl=%q html=%q", tmpl, html)
	}
}

func TestRenderPage_IndexJSON(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	ctx, w := newRenderCtx(t, "/plugins/index.json", "index.json", settings)
	if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" {
		t.Errorf("index.json should be written directly, got template %q", tmpl)
	}
	raw, _, _ := p.fetcher.Index()
	if w.Code != http.StatusOK || w.Body.String() != string(raw) {
		t.Errorf("index.json must be served verbatim: code=%d body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("cache-control %q", cc)
	}

	// Nothing cached: 503 JSON error.
	p2 := New()
	p2.fetcher = NewFetcher(&http.Client{Timeout: time.Second})
	ctx, w = newRenderCtx(t, "/plugins/index.json", "index.json", map[string]string{"index_url": "http://127.0.0.1:1/index.json"})
	p2.RenderPage(ctx, PageType)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "error") {
		t.Errorf("expected 503 JSON error, got %d %q", w.Code, w.Body.String())
	}
}

func TestRenderPage_Detail(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	ctx, _ := newRenderCtx(t, "/plugins/hello", "hello", settings)
	tmpl, data := p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || data["title"] != "Hello" {
		t.Fatalf("tmpl=%q title=%v", tmpl, data["title"])
	}
	html, _ := data["plugin_content"].(string)
	for _, want := range []string{"<h1>Hello</h1>", "<p>First</p>", "abc", "0.2.6", `href="https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go"`, `href="/plugins"`, "2026-09-14"} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q in:\n%s", want, html)
		}
	}
	if strings.Contains(html, "Changelog") {
		t.Error("empty changelog should not render a Changelog section")
	}

	// Unknown, invalid and nested names are declined (blog turns that into a 404).
	for _, sub := range []string{"nope", "Bad Name", "../x", "hello/extra"} {
		ctx, w := newRenderCtx(t, "/plugins/"+sub, sub, settings)
		if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" || w.Body.Len() != 0 {
			t.Errorf("%q: expected to be declined, got tmpl=%q body=%q", sub, tmpl, w.Body.String())
		}
	}
}

func TestRenderPage_DetailFetchFails(t *testing.T) {
	srv := newFixtureServer(t)
	srv.detail.Store(func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins/hello", "hello", map[string]string{"index_url": srv.URL + "/index.json"})
	tmpl, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if tmpl != "page_content.html" || !strings.Contains(html, "unavailable") || !strings.Contains(html, "Hello") {
		t.Errorf("a listed plugin whose detail fails should still show its index entry with a notice, got %q", html)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./plugins/directory/ -run 'TestRenderPage' -v`
Expected: FAIL — the stub returns `""` so `TestRenderPage_Listing` fails with `tmpl="" data=map[]` (and the rest similarly).

- [ ] **Step 3: Add the templates**

`plugins/directory/templates/listing.html`:

```html
<p>{{ len .Entries }} plugin{{ if ne (len .Entries) 1 }}s{{ end }} in the directory.
Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.
To publish your own, see the <a href="https://github.com/goblogplatform/plugins">registry</a>.</p>
{{ if .Entries }}
<table class="table">
  <thead>
    <tr><th>Plugin</th><th>Description</th><th>Version</th><th>Author</th><th>License</th><th>Type</th></tr>
  </thead>
  <tbody>
  {{ range .Entries }}
    <tr>
      <td><a href="{{ $.Base }}/{{ .Name }}">{{ .DisplayName }}</a><br><small><a href="{{ .SourceURL }}">source</a></small></td>
      <td>{{ .Description }}</td>
      <td>{{ .Version }}</td>
      <td>{{ .Author }}</td>
      <td>{{ .License }}</td>
      <td>{{ .InstallType }}</td>
    </tr>
  {{ end }}
  </tbody>
</table>
{{ end }}
```

`plugins/directory/templates/detail.html` (`date` is a template function defined in `render.go` below):

```html
<p><a href="{{ .Base }}">&larr; All plugins</a></p>
<h2>{{ .Entry.DisplayName }} <small>v{{ .Entry.Version }}</small></h2>
<p>{{ .Entry.Description }}</p>
<dl>
  <dt>Version</dt><dd>{{ .Entry.Version }}{{ if .Entry.ReleasedAt }} <small>(released {{ date .Entry.ReleasedAt }})</small>{{ end }}</dd>
  <dt>Author</dt><dd>{{ .Entry.Author }}</dd>
  <dt>License</dt><dd>{{ .Entry.License }}</dd>
  <dt>Source</dt><dd><a href="{{ .Entry.SourceURL }}">{{ .Entry.SourceURL }}</a></dd>
  <dt>Requires</dt><dd>goblog {{ .Entry.MinGoblogVersion }} or newer ({{ .Entry.InstallType }} plugin)</dd>
  <dt>Download</dt><dd><a href="{{ .Entry.DownloadURL }}">{{ .Entry.DownloadURL }}</a><br><small>sha256 <code>{{ .Entry.SHA256 }}</code></small></dd>
</dl>
{{ if .Notice }}<div class="alert alert-warning" role="alert">{{ .Notice }}</div>{{ end }}
{{ if .Detail }}
<h2>README</h2>
<div class="plugin-readme">{{ .Detail.ReadmeHTML }}</div>
{{ if .Detail.ChangelogHTML }}
<h2>Changelog</h2>
<div class="plugin-changelog">{{ .Detail.ChangelogHTML }}</div>
{{ end }}
{{ if .Detail.Releases }}
<h2>Releases</h2>
{{ range .Detail.Releases }}
<h3><a href="{{ .URL }}">v{{ .Version }}</a>{{ if .ReleasedAt }} <small>{{ date .ReleasedAt }}</small>{{ end }}</h3>
<div class="plugin-release-notes">{{ .NotesHTML }}</div>
{{ end }}
{{ end }}
{{ end }}
```

- [ ] **Step 4: Implement rendering**

`plugins/directory/render.go`:

```go
package directory

import (
	"bytes"
	"embed"
	"html/template"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates are html/template, so every index string is escaped and unsafe
// URL schemes are neutralised; only the *_html fields (template.HTML) pass
// through unchanged.
var templates = template.Must(template.New("").Funcs(template.FuncMap{
	// date shows the day part of an RFC 3339 timestamp, or the value as-is.
	"date": func(s string) string {
		if len(s) >= 10 {
			return s[:10]
		}
		return s
	},
}).ParseFS(templateFS, "templates/*.html"))

// namePattern is the registry's rule for plugin names; anything else in a
// sub-path is not a plugin page.
var namePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func validName(name string) bool { return namePattern.MatchString(name) }

// basePath is the page's URL prefix ("/plugins"), taken from the request so
// links keep working if the admin renames the page's slug.
func basePath(c *gin.Context) string {
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	slug, _, _ := strings.Cut(path, "/")
	return "/" + slug
}

func renderListing(base string, entries []Entry) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "listing.html", map[string]any{"Base": base, "Entries": entries})
	return buf.String(), err
}

// renderDetail renders a plugin page. d may be nil when the detail JSON could
// not be fetched; notice is shown to the reader in that case.
func renderDetail(base string, e Entry, d *Detail, notice string) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "detail.html", map[string]any{
		"Base": base, "Entry": e, "Detail": d, "Notice": notice,
	})
	return buf.String(), err
}
```

Replace the `RenderPage` stub in `plugins/directory/directory.go` with:

```go
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
	p.fetcher.Ensure(indexURL(ctx.Settings))
	base := basePath(c)

	switch {
	case ctx.SubPath == "":
		_, entries, ok := p.fetcher.Index()
		if !ok {
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		html, err := renderListing(base, entries)
		if err != nil {
			log.Printf("Directory plugin: render listing: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html}

	case ctx.SubPath == "index.json":
		raw, _, ok := p.fetcher.Index()
		if !ok {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin directory index unavailable"})
			return "", nil
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case validName(ctx.SubPath):
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
```

Add `"net/http"` to `directory.go`'s imports.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./plugins/directory/ -v`
Expected: PASS for every test in the package. If `TestRenderPage_ListingEscapesIndexStrings` fails on the `javascript:` check, confirm the template uses `href="{{ .SourceURL }}"` (html/template rewrites unsafe schemes to `#ZgotmplZ` only inside attribute context).

Run: `go vet ./plugins/directory/`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git branch --show-current
git add plugins/directory/
git commit -m "Render the plugin directory listing, detail pages and index.json (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Register the plugin, document it, verify end to end, open the PR

**Files:**
- Modify: `goblog.go:11-14` (imports) and `:294-298` (registration)
- Modify: `README.md` — "### Plugins" feature bullets (line ~42-45) and a new "### Plugin directory" subsection under "## Plugins" (after "### Dynamic plugins", before "## Testing")
- Modify: `plugins/dynamic/hello.go.example` header comment

- [ ] **Step 1: Register the plugin**

In `goblog.go` imports add `"goblog/plugins/directory"` (keep the alphabetical group: `analytics`, `directory`, `scholar`, `socialicons`), and after `registry.Register(scholarplugin.New())` add:

```go
	registry.Register(directory.New())
```

- [ ] **Step 2: Document**

README "### Plugins" feature list — change the built-in plugins bullet to:

```markdown
- Built-in plugins: `analytics`, `socialicons`, `scholar` (research page; Google Scholar is blocked from most cloud IPs, so set its `source` setting to `semantic_scholar` when hosting in a datacenter), `directory` (the plugin directory that runs [goblog.live/plugins](https://goblog.live/plugins); off by default)
```

Add after the "#### Checking a plugin file" block (Task 2) and before `## Testing`:

```markdown
### Plugin directory
[goblog.live/plugins](https://goblog.live/plugins) lists published dynamic plugins; `https://goblog.live/plugins/index.json` is the same list as JSON (name, version, author, license, `download_url`, `sha256`, `min_goblog_version`). Plugins are individual GitHub repositories with releases; the curated list and the build that produces the index live in [goblogplatform/plugins](https://github.com/goblogplatform/plugins), which also documents how to submit one.

The pages are rendered by the built-in `directory` plugin, which any goblog can turn on under **Admin → Settings → Plugin Directory** (`enabled` = `true`). It fetches `index_url` every `refresh_minutes`, keeps the last good copy if the registry is unreachable, and serves `/plugins`, `/plugins/<name>` and `/plugins/index.json`.
```

`plugins/dynamic/hello.go.example` — add one line to the header comment after the "To try it" steps:

```go
// The same plugin, packaged the way the plugin directory expects (manifest,
// README, tagged releases), is at github.com/goblogplatform/goblog-plugin-hello.
```

- [ ] **Step 3: Full verification**

Run: `go build -v . && go vet ./... && go test -race goblog/...`
Expected: build ok, vet silent, all packages PASS.

Run the app against a fixture index to see it working:

```bash
mkdir -p /tmp/claude-1000/-home-jason-dev-goblog/*/scratchpad/dirfix/plugins 2>/dev/null || true
FIX=$(ls -d /tmp/claude-1000/-home-jason-dev-goblog/*/scratchpad | head -1)/dirfix
mkdir -p "$FIX/plugins"
cat > "$FIX/index.json" <<'EOF'
[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason Ernst","license":"GPL-3.0","source_url":"https://github.com/goblogplatform/goblog-plugin-hello","download_url":"https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go","sha256":"deadbeef","min_goblog_version":"0.2.6","install_type":"dynamic","released_at":"2026-09-14T00:00:00Z","detail_url":"http://127.0.0.1:8099/plugins/hello.json"}]
EOF
cat > "$FIX/plugins/hello.json" <<'EOF'
{"name":"hello","display_name":"Hello","version":"1.0.0","readme_html":"<h1>Hello</h1><p>It says hi.</p>","changelog_html":"","releases":[{"version":"1.0.0","released_at":"2026-09-14T00:00:00Z","notes_html":"<p>First release</p>","url":"https://github.com/goblogplatform/goblog-plugin-hello/releases/tag/v1.0.0"}]}
EOF
(cd "$FIX" && python3 -m http.server 8099 >/dev/null 2>&1 &)
```

Then start goblog with the project's usual local steps (see README "Quick Start → Local"; a fresh sqlite DB and the wizard are fine), log in as admin, set **Plugin Directory → enabled** to `true` and `index_url` to `http://127.0.0.1:8099/index.json`, and check:

- `curl -s localhost:7000/plugins | grep -c Hello` → ≥ 1
- `curl -si localhost:7000/plugins/index.json | grep -E 'Content-Type|Cache-Control|"name"'` → JSON content type, `max-age=300`, the entry
- `curl -s localhost:7000/plugins/hello | grep -c 'First release'` → ≥ 1
- `curl -s -o /dev/null -w '%{http_code}' localhost:7000/plugins/nope` → `404`
- Set `enabled` back to `false`: `/plugins` → 404 "currently disabled" and it leaves the nav.

Kill the fixture server afterwards (`pkill -f 'http.server 8099'`).

- [ ] **Step 4: Commit and open the PR**

```bash
git branch --show-current
git add goblog.go README.md plugins/dynamic/hello.go.example
git commit -m "Register the directory plugin and document the plugin directory (#552)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/552-plugin-directory
gh pr create --base main --title "Plugin directory: validate-plugin command and directory plugin (#552)" --body "$(cat <<'EOF'
## Summary
goblog side of #552 (the registry repo and seed plugin repo follow separately):
- `goblog validate-plugin <file.go>` loads a dynamic plugin through the Yaegi loader and prints its identity as JSON — what the registry CI will run on submissions.
- Plugin-owned pages now own their sub-paths (`ctx.SubPath`) and may write raw responses; blog routes `/<slug>/<rest>` to the plugin and 404s declined sub-paths.
- New compiled-in `directory` plugin (off by default): fetches the registry's `index.json`, keeps the last good copy, renders `/plugins`, `/plugins/<name>` and re-serves `/plugins/index.json`.
- Design: `docs/superpowers/specs/2026-09-14-plugin-directory-design.md`; plan: `docs/superpowers/plans/2026-09-14-plugin-directory-goblog.md`.

## Test plan
- [ ] `go test -race goblog/...` green
- [ ] Manual: enable the plugin against a fixture index, check listing / detail / index.json / 404 / disabled (Task 8 in the plan)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Do **not** merge the PR.

---

## Self-review notes

- Spec coverage: A (Tasks 1–2), sub-paths + raw responses (Tasks 3–4), fetcher (5), plugin settings/page/job (6), RenderPage incl. escaping, verbatim index, 503, unknown-name 404 (7), registration + docs + end-to-end (8). The README hook-table update (spec "documented in the README") is in Task 4; `validate-plugin` docs in Task 2; directory docs in Task 8.
- Type consistency: `RenderPluginPage(c, pageType, subPath)` (Task 3) is what Task 4's `pluginRegistry` interface declares; `Fetcher` method names in Task 5 match their uses in Tasks 6–7; `PageType` constant is used by tests in Tasks 6–7; `newFixtureServer` from Task 5's test file is reused by Task 6/7 tests (same package).
- Dates are shown via the `date` template func (first 10 chars, guarded) rather than the `slice` builtin, which panics on short strings.
