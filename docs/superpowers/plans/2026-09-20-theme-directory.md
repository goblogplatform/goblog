# Theme directory — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** goblog.live/themes showcases themes submitted by anyone and approved by an admin, and any goblog installs, updates, activates and removes them from Admin → Themes.

**Architecture:** A new `theme` package owns theme resolution and loading (built-in `themes/<name>` first, then `themes/installed/<name>`; a theme's templates are parsed on top of `themes/default`, so partial themes work). The `directory` plugin gains a `kind` (`plugin`/`theme`) — same tables, service and admin tab — and `registry` gains a theme validator/builder that parses GitHub's tag zipball in memory and hashes its content. A new `theme/installer` package mirrors `plugin/installer` (download → hash → validate → unpack) and feeds an Admin → Themes page. Delivered as three goblog PRs (loader; directory kind + pages; installer + admin), then the forest theme repo, the site-theme template and iac mounts.

**Tech Stack:** Go 1.25, gin, gorm (sqlite in tests), `html/template`, `archive/zip`, vanilla JS in admin templates. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-09-20-theme-directory-design.md`

## Global Constraints

- Theme name rule for the directory: `^[a-z0-9-]+$`, and never `default`, `minimal`, `forest`, `installed`, `shared`. On disk the loader accepts the existing `^[A-Za-z0-9_-]+$`; `installed` and `shared` are reserved there too.
- Installed themes live in `THEMES_INSTALLED_DIR` (default `themes/installed`); `theme.Dir(name)` resolves `themes/<name>` first, then the installed root; both need a `templates/` directory to count.
- Loader order: `templates/shared/*.html` → `themes/default/templates/*.html` → the active theme's `templates/*.html` (override by name). `/theme/*` static files fall back to `themes/default/static`.
- Archive limits: ≤ 16 MiB compressed (`MaxAssetBytes`), ≤ 2000 entries (`MaxArchiveEntries`), no symlinks, no `..`/absolute paths; only `templates/**` and `static/**` are kept; GitHub's single top-level folder is stripped. Screenshot `screenshot.png` or `screenshot.jpg` ≤ 1 MiB (`MaxScreenshotBytes`; the contents API cannot return larger files inline — deviation from the spec's 2 MiB, patched into the spec in Task 8).
- `sha256` for a theme = `ContentHash(files)`: sha256 over, for each path in sorted order, `path + "\x00" + decimal length + "\x00" + bytes`. `download_url` = `https://github.com/<owner>/<repo>/archive/refs/tags/<tag>.zip`; `screenshot_url` = `https://raw.githubusercontent.com/<owner>/<repo>/<tag>/screenshot.<ext>`.
- Index/detail JSON field names are the contract: existing plugin fields unchanged; new `kind` (always present: `"plugin"`/`"theme"`), `screenshot_url` (`omitempty`). Themes: `install_type: "theme"`, `runtime: ""`, `allowed_hosts: []`. `/plugins/index.json` lists plugins only; `/themes/index.json` themes only.
- Theme `min_goblog_version` floor is `0.5.0` (the release with the override loader). `theme_directory_url` default `https://www.goblog.live/themes/index.json`.
- Admin endpoints are admin-only JSON; the existing `requireJSON()` middleware covers `/api/v1/themes/*`.
- Every task: `gofmt -l` clean on touched packages, `go vet` on touched packages (ignore the pre-existing `blog/blog.go:102` lock-copy finding), `go test ./...` green, commit ends with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Never push to `main`.
- Branches: PR 1 `feat/theme-loader`, PR 2 `feat/theme-directory` (off PR 1's branch until it merges, then rebased), PR 3 `feat/theme-installer` (off PR 2's). Merge the spec branch `docs/theme-directory-spec` into PR 1's branch first so the spec travels with the code.

## File structure

```
theme/                                   NEW — PR 1
  theme.go, theme_test.go                 roots, ValidName, Dir, List
  load.go, load_test.go                   Load (override), ValidateFiles, StaticHandler
goblog.go                                MODIFY — PR 1 (use theme.*), PR 3 (installer wiring, routes)
admin/admin.go                           MODIFY — PR 1 (ListThemes → theme.List), PR 3 (Themes field)
README.md                                MODIFY — PR 1 (Theming section), PR 3 (Admin → Themes)
plugins/directory/registry/
  theme.go, theme_test.go                 NEW — PR 2: manifest, ParseArchive, ContentHash, ValidateThemeEntry, BuildTheme
  source.go, source_test.go               MODIFY — Zipball, FileURL
  build.go                                MODIFY — Kind/ScreenshotURL on IndexEntry, renderDocs helper
  validate_test.go                        MODIFY — memSource gains Zipball/FileURL
plugins/directory/
  store.go, store_test.go                 MODIFY — Kind columns, migration, approvedDocs(kind)
  service.go, service_test.go             MODIFY — kind-aware API, per-kind cache
  directory.go, directory_test.go         MODIFY — second page, RenderPage by kind
  submit.go, submit_test.go               MODIFY — kind in submitView/renderSubmit
  render.go                               MODIFY — renderThemesListing, renderThemeDetail
  templates/themes-listing.html, themes-detail.html   NEW
  templates/submit.html                   MODIFY
admin/directory.go, directory_test.go    MODIFY — kind in Add; stubSource gains methods
themes/default/templates/admin_plugins.html  MODIFY — kind badge/radio/screenshot (PR 2)
docs/THEME_CONTRACT.md                   NEW — PR 2
theme/installer/                         NEW — PR 3
  installer.go, installer_test.go
admin/themes.go, themes_test.go          NEW — PR 3
themes/default/templates/admin_themes.html  NEW — PR 3; admin_nav.html MODIFY
tools/migrate.go                         MODIFY — theme_directory_url default (PR 3)
csrf_test.go                             MODIFY — themes rows (PR 3)
```

---

## PR 1 — theme loader

### Task 1: `theme` package — roots, names, resolution, listing

**Files:**
- Create: `theme/theme.go`, `theme/theme_test.go`

**Interfaces:**
- Produces:
  ```go
  package theme
  var BuiltinRoot = "themes"            // package vars so tests can point at temp dirs
  var SharedDir = "templates/shared"
  const DefaultName = "default"
  func InstalledRoot() string           // $THEMES_INSTALLED_DIR or BuiltinRoot/installed
  func ValidName(name string) bool      // ^[A-Za-z0-9_-]+$ and not reserved
  func Dir(name string) (dir string, ok bool)
  func IsBuiltin(name string) bool
  func List() []string                  // sorted, always contains "default"
  ```

- [ ] **Step 1: Write the failing tests**

```go
package theme

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// roots points BuiltinRoot and the installed root at fresh temp dirs and
// creates a "default" theme in the built-in root.
func roots(t *testing.T) (builtin, installed string) {
	t.Helper()
	builtin, installed = t.TempDir(), t.TempDir()
	old := BuiltinRoot
	BuiltinRoot = builtin
	t.Cleanup(func() { BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "default home"})
	return builtin, installed
}

// mkTheme writes templates/<name> files under dir.
func mkTheme(t *testing.T, dir string, templates map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range templates {
		if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstalledRoot(t *testing.T) {
	t.Setenv("THEMES_INSTALLED_DIR", "")
	if got := InstalledRoot(); got != filepath.Join(BuiltinRoot, "installed") {
		t.Errorf("default installed root = %q", got)
	}
	t.Setenv("THEMES_INSTALLED_DIR", "/srv/themes")
	if got := InstalledRoot(); got != "/srv/themes" {
		t.Errorf("env installed root = %q", got)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"default": true, "my-Theme_2": true, "": false, "../x": false, "a b": false, "installed": false, "shared": false} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDirAndList(t *testing.T) {
	builtin, installed := roots(t)
	mkTheme(t, filepath.Join(builtin, "forest"), map[string]string{"home.html": "f"})
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "o"})
	mkTheme(t, filepath.Join(installed, "forest"), map[string]string{"home.html": "shadowed"})
	os.MkdirAll(filepath.Join(builtin, "notatheme"), 0o755)          // no templates/ → ignored
	os.MkdirAll(filepath.Join(installed, "installed", "templates"), 0o755) // reserved name → ignored

	if dir, ok := Dir("forest"); !ok || dir != filepath.Join(builtin, "forest") {
		t.Errorf("built-in wins: %q %v", dir, ok)
	}
	if dir, ok := Dir("ocean"); !ok || dir != filepath.Join(installed, "ocean") {
		t.Errorf("installed resolves: %q %v", dir, ok)
	}
	for _, bad := range []string{"nope", "notatheme", "../default", "installed"} {
		if _, ok := Dir(bad); ok {
			t.Errorf("Dir(%q) should not resolve", bad)
		}
	}
	if !IsBuiltin("forest") || IsBuiltin("ocean") || IsBuiltin("nope") {
		t.Error("IsBuiltin wrong")
	}
	if got := List(); !reflect.DeepEqual(got, []string{"default", "forest", "ocean"}) {
		t.Errorf("List = %v", got)
	}
}

func TestListWithoutRoots(t *testing.T) {
	old := BuiltinRoot
	BuiltinRoot = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", filepath.Join(t.TempDir(), "missing"))
	if got := List(); !reflect.DeepEqual(got, []string{"default"}) {
		t.Errorf("List with no roots = %v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./theme/`
Expected: FAIL — package does not exist / undefined symbols.

- [ ] **Step 3: Write `theme/theme.go`**

```go
// Package theme resolves, lists and loads goblog themes. A theme is a
// directory with templates/ (and optionally static/). Built-in themes ship
// in the image under themes/; themes installed from the directory live in
// a separate, persisted root so a Docker bind mount can keep them across
// redeploys without hiding the built-ins.
package theme

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// BuiltinRoot holds the themes compiled into the image (and, on bare
// metal, whatever the operator drops there). SharedDir holds the templates
// every theme gets. Both are package variables so tests can point them at
// temp dirs.
var (
	BuiltinRoot = "themes"
	SharedDir   = "templates/shared"
)

// DefaultName is the theme every other theme is layered on.
const DefaultName = "default"

// InstalledRoot is where the theme installer writes: $THEMES_INSTALLED_DIR
// or themes/installed. Read on every call so tests can change it.
func InstalledRoot() string {
	if v := os.Getenv("THEMES_INSTALLED_DIR"); v != "" {
		return v
	}
	return filepath.Join(BuiltinRoot, "installed")
}

// namePattern is the on-disk rule, unchanged from the old inline check in
// main: a theme name is a single safe path segment.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// reserved are directory names under a root that are not themes.
var reserved = map[string]bool{"installed": true, "shared": true}

// ValidName reports whether name may be looked up on disk.
func ValidName(name string) bool { return namePattern.MatchString(name) && !reserved[name] }

// Dir returns the directory of a theme — the built-in one first, then the
// installed one — and whether it exists. A directory counts only when it
// has a templates/ subdirectory.
func Dir(name string) (string, bool) {
	if !ValidName(name) {
		return "", false
	}
	for _, root := range []string{BuiltinRoot, InstalledRoot()} {
		dir := filepath.Join(root, name)
		if hasTemplates(dir) {
			return dir, true
		}
	}
	return "", false
}

// IsBuiltin reports whether name resolves to the built-in root.
func IsBuiltin(name string) bool {
	return ValidName(name) && hasTemplates(filepath.Join(BuiltinRoot, name))
}

// List returns every theme name from both roots, sorted, "default" always
// included (the loader falls back to it even if the directory is missing).
func List() []string {
	seen := map[string]bool{DefaultName: true}
	for _, root := range []string{BuiltinRoot, InstalledRoot()} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && ValidName(e.Name()) && hasTemplates(filepath.Join(root, e.Name())) {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func hasTemplates(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "templates"))
	return err == nil && info.IsDir()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./theme/ -v`
Expected: PASS ×4.

- [ ] **Step 5: Commit**

```bash
git add theme && git commit -m "theme: resolve and list themes across the built-in and installed roots

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `theme.Load` (override by name), `ValidateFiles`, `StaticHandler`

**Files:**
- Create: `theme/load.go`, `theme/load_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  ```go
  func Load(name string, funcMap template.FuncMap) (tmpl *template.Template, loaded string, err error)
  func ValidateFiles(files map[string][]byte) error
  func StaticHandler(active func() string) gin.HandlerFunc
  ```
  `Load` returns `err` only when shared or default cannot be parsed (fatal at startup); a broken or missing named theme logs and returns default with `loaded == "default"`.

- [ ] **Step 1: Write the failing tests**

```go
package theme

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func withShared(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := SharedDir
	SharedDir = dir
	t.Cleanup(func() { SharedDir = old })
	os.WriteFile(filepath.Join(dir, "shared.html"), []byte(`{{ define "shared" }}SHARED{{ end }}`), 0o644)
}

func render(t *testing.T, tmpl *template.Template, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, nil); err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return buf.String()
}

func TestLoad_OverridesByName(t *testing.T) {
	builtin, installed := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "default home", "post.html": `default post {{ template "shared" }}`})
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "ocean home"})

	tmpl, loaded, err := Load("ocean", nil)
	if err != nil || loaded != "ocean" {
		t.Fatalf("Load: %q %v", loaded, err)
	}
	if got := render(t, tmpl, "home.html"); got != "ocean home" {
		t.Errorf("theme template should win: %q", got)
	}
	if got := render(t, tmpl, "post.html"); got != "default post SHARED" {
		t.Errorf("missing templates fall back to default (with shared): %q", got)
	}
}

func TestLoad_FallsBackToDefault(t *testing.T) {
	builtin, installed := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(installed, "broken"), map[string]string{"home.html": "{{ if }}"})
	_ = builtin
	for _, name := range []string{"broken", "missing", "../x"} {
		tmpl, loaded, err := Load(name, nil)
		if err != nil || loaded != DefaultName {
			t.Errorf("Load(%q) = %q, %v; want default", name, loaded, err)
		}
		if got := render(t, tmpl, "home.html"); got != "default home" {
			t.Errorf("Load(%q) rendered %q", name, got)
		}
	}
}

func TestLoad_FuncMapAndBrokenDefault(t *testing.T) {
	builtin, _ := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": `{{ shout "hi" }}`})
	tmpl, _, err := Load(DefaultName, template.FuncMap{"shout": strings.ToUpper})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, tmpl, "home.html"); got != "HI" {
		t.Errorf("funcMap not applied: %q", got)
	}
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "{{ if }}"})
	if _, _, err := Load(DefaultName, nil); err == nil {
		t.Error("a broken default theme must be an error, not a silent fallback")
	}
}

func TestValidateFiles(t *testing.T) {
	roots(t)
	withShared(t)
	ok := map[string][]byte{"templates/home.html": []byte(`{{ template "shared" }} ok`), "static/a.css": []byte("x"), "templates/sub/ignored.html": []byte("{{ if }}")}
	if err := ValidateFiles(ok); err != nil {
		t.Errorf("valid theme rejected: %v", err)
	}
	bad := map[string][]byte{"templates/home.html": []byte("{{ if }}")}
	if err := ValidateFiles(bad); err == nil || !strings.Contains(err.Error(), "templates/home.html") {
		t.Errorf("want the broken file named, got %v", err)
	}
	if err := ValidateFiles(map[string][]byte{"static/only.css": []byte("x")}); err == nil {
		t.Error("a theme with no templates is not a theme")
	}
}

func TestStaticHandler_FallsBackToDefault(t *testing.T) {
	builtin, installed := roots(t)
	os.MkdirAll(filepath.Join(builtin, DefaultName, "static", "css"), 0o755)
	os.WriteFile(filepath.Join(builtin, DefaultName, "static", "css", "base.css"), []byte("base"), 0o644)
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "o"})
	os.MkdirAll(filepath.Join(installed, "ocean", "static", "css"), 0o755)
	os.WriteFile(filepath.Join(installed, "ocean", "static", "css", "theme.css"), []byte("ocean"), 0o644)
	os.WriteFile(filepath.Join(builtin, "secret.txt"), []byte("no"), 0o644)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/theme/*filepath", StaticHandler(func() string { return "ocean" }))
	get := func(p string) (int, string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w.Code, w.Body.String()
	}
	if code, body := get("/theme/css/theme.css"); code != 200 || body != "ocean" {
		t.Errorf("theme file: %d %q", code, body)
	}
	if code, body := get("/theme/css/base.css"); code != 200 || body != "base" {
		t.Errorf("default fallback: %d %q", code, body)
	}
	if code, _ := get("/theme/css/nope.css"); code != 404 {
		t.Errorf("missing: %d", code)
	}
	if code, _ := get("/theme/../../secret.txt"); code == 200 {
		t.Error("traversal must not escape the static dir")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./theme/ -run 'Load|ValidateFiles|StaticHandler'`
Expected: FAIL — undefined `Load` etc.

- [ ] **Step 3: Write `theme/load.go`**

```go
package theme

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// base parses the templates every theme starts from: the shared set, then
// the default theme. A theme's own files are parsed on top, so a template
// it does not ship falls back to default's instead of 500ing the page.
func base(funcMap template.FuncMap) (*template.Template, error) {
	tmpl, err := template.New("").Funcs(funcMap).ParseGlob(filepath.Join(SharedDir, "*.html"))
	if err != nil {
		return nil, fmt.Errorf("shared templates: %w", err)
	}
	if tmpl, err = tmpl.ParseGlob(filepath.Join(BuiltinRoot, DefaultName, "templates", "*.html")); err != nil {
		return nil, fmt.Errorf("default theme: %w", err)
	}
	return tmpl, nil
}

// Load builds the template set for name. Anything wrong with the named
// theme (unknown, unparsable) is logged and default is loaded instead;
// only a broken shared/default set is an error, because nothing can render
// without it.
func Load(name string, funcMap template.FuncMap) (*template.Template, string, error) {
	tmpl, err := base(funcMap)
	if err != nil {
		return nil, "", err
	}
	if name == DefaultName {
		return tmpl, DefaultName, nil
	}
	dir, ok := Dir(name)
	if !ok {
		log.Printf("Warning: theme %q is invalid or missing, falling back to default", name)
		return tmpl, DefaultName, nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "templates", "*.html"))
	if len(matches) == 0 {
		// The theme directory exists but ships no templates yet; it renders
		// exactly like default, which is a valid (if empty) override.
		return tmpl, name, nil
	}
	if _, err := tmpl.ParseFiles(matches...); err != nil {
		log.Printf("Warning: failed to load theme %q: %v — falling back to default", name, err)
		tmpl, err = base(funcMap)
		return tmpl, DefaultName, err
	}
	return tmpl, name, nil
}

// ValidateFiles checks a theme's files as the installer and the directory
// see them (paths relative to the theme root, e.g. "templates/home.html"):
// there must be at least one template, and each templates/*.html must parse
// on top of the shared and default sets. Files in subdirectories of
// templates/ are not loaded by Load and are ignored here too.
func ValidateFiles(files map[string][]byte) error {
	tmpl, err := base(template.FuncMap{"rawHTML": func(s string) template.HTML { return template.HTML(s) }})
	if err != nil {
		return err
	}
	var names []string
	for p := range files {
		if path.Dir(p) == "templates" && strings.HasSuffix(p, ".html") {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return errors.New("no templates/*.html files: a theme must ship at least one template")
	}
	sort.Strings(names) // deterministic first error
	for _, p := range names {
		if _, err := tmpl.New(path.Base(p)).Parse(string(files[p])); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// StaticHandler serves /theme/* from the active theme's static/ directory,
// falling back to default's so an override-only theme still gets the base
// CSS. The path is cleaned as an absolute path first so ".." cannot climb
// out of static/.
func StaticHandler(active func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		fp := path.Clean("/" + c.Param("filepath"))
		if dir, ok := Dir(active()); ok {
			if p := filepath.Join(dir, "static", filepath.FromSlash(fp)); isFile(p) {
				c.File(p)
				return
			}
		}
		if p := filepath.Join(BuiltinRoot, DefaultName, "static", filepath.FromSlash(fp)); isFile(p) {
			c.File(p)
			return
		}
		c.Status(http.StatusNotFound)
	}
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./theme/ -v`
Expected: PASS. If `TestLoad_OverridesByName` fails on `post.html` with "no such template shared", the shared glob did not match — check `SharedDir` is the temp dir in `withShared`.

- [ ] **Step 5: Commit**

```bash
git add theme && git commit -m "theme: load themes on top of default, validate template files, serve static with fallback

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Wire the loader into main, `ListThemes`, README; open PR 1

**Files:**
- Modify: `goblog.go` (the theme block ~lines 362–420), `admin/admin.go` (`ListThemes`), `README.md` (Theming section)

**Interfaces:**
- Consumes: `theme.Load`, `theme.List`, `theme.StaticHandler`, `theme.InstalledRoot`.

- [ ] **Step 1: Replace the inline theme code in `goblog.go`**

Delete `isValidTheme` and the body of `loadTheme`; the block becomes:

```go
	funcMap := template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}
	router.SetFuncMap(funcMap)

	loadTheme := func(name string) {
		tmpl, loaded, err := theme.Load(name, funcMap)
		if err != nil {
			log.Fatalf("Failed to load templates: %v", err)
		}
		log.Println("Loading theme: " + loaded)
		router.SetHTMLTemplate(tmpl)
		activeTheme = loaded
	}
	loadTheme(activeTheme)

	// Wire up hot-reload callback so theme changes take effect without restart
	_admin.OnThemeChange = func(theme string) {
		loadTheme(theme)
	}

	// Theme static files: the active theme's static/, falling back to default's
	router.GET("/theme/*filepath", theme.StaticHandler(func() string { return activeTheme }))
```

Add `"goblog/theme"` to the imports; drop `path/filepath` only if nothing else in the file uses it (`go build` tells you).

- [ ] **Step 2: `ListThemes` delegates**

```go
// ListThemes returns every theme goblog can activate: built-in and installed.
func ListThemes() []string { return theme.List() }
```

(import `"goblog/theme"`; remove the now-unused `os` import if that was its only use — check with `go build ./admin/`).

- [ ] **Step 3: README**

Replace the "## Theming" section with:

```markdown
## Theming

Themes live in `themes/{name}/` with this structure:
```
themes/
  default/
    templates/    # HTML templates
    static/       # CSS and assets (served at /theme/)
  installed/      # themes installed from the directory (THEMES_INSTALLED_DIR; bind-mount it in Docker)
    ocean/
      templates/
      static/
```

A theme's templates are loaded **on top of `themes/default`**: it only has to ship the templates it changes, and everything else — including admin pages added by newer goblog releases — renders from default. `/theme/<file>` serves the active theme's `static/` and falls back to default's.

To create a custom theme:
1. Create `themes/my-theme/templates/` and copy in only the templates you want to change (start with `header.html`, `footer.html`, `home.html`); add `static/` for CSS.
2. Set the `theme` setting to `my-theme` in admin settings (hot-reloads, no restart).

Themes from the directory are installed under **Admin → Themes** into `themes/installed/`; see [docs/THEME_CONTRACT.md](docs/THEME_CONTRACT.md) to publish one.
```

(The contract doc lands in PR 2; the link is fine to add now.)

- [ ] **Step 4: Build, test, run once**

Run: `go build ./... && go vet ./theme/ ./admin/ && go test ./...`
Expected: green. Then `go build -o /tmp/goblog-theme-check . && (cd /tmp && rm -f goblog-theme-check)` is unnecessary — instead start the app briefly from the repo root with a scratch sqlite `.env` (see `template.env`), curl `/` and `/theme/css/style.css` (or whichever file `themes/default/static/css` has) and confirm 200s, then stop it. Note the result in the commit body.

- [ ] **Step 5: Commit and open PR 1**

```bash
git add goblog.go admin/admin.go README.md && git commit -m "Load themes through the theme package: override default by name, static fallback, installed root

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/theme-loader
gh pr create --base main --title "Theme loader: override-by-name, installed root, static fallback" --body "$(cat <<'BODY'
Part 1 of the theme directory (#560, spec `docs/superpowers/specs/2026-09-20-theme-directory-design.md`).

- New `theme` package: `Dir`/`List` resolve themes from `themes/<name>` first, then `THEMES_INSTALLED_DIR` (default `themes/installed`); `Load` parses shared → default → the theme, so a theme only ships what it overrides (fixes `minimal` 500ing on admin pages it lacks); `StaticHandler` serves `/theme/*` with a default fallback and no path traversal.
- `goblog.go` and `admin.ListThemes` use it; README Theming section rewritten.

Behaviour change: a theme missing a template now renders default's instead of erroring.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

## PR 2 — registry theme support, directory kind, /themes pages

Branch `feat/theme-directory` from `feat/theme-loader` (rebase onto `main` once PR 1 merges).

### Task 4: `registry` — theme manifest, archive parsing, content hash

**Files:**
- Create: `plugins/directory/registry/theme.go`, `plugins/directory/registry/theme_test.go`

**Interfaces:**
- Consumes: `NamePattern`, `versionPattern`, `knownLicenses` (manifest.go), `MaxAssetBytes` (source.go).
- Produces:
  ```go
  const ( KindPlugin = "plugin"; KindTheme = "theme" )
  const MaxArchiveEntries = 2000
  const MaxScreenshotBytes = 1 << 20
  type ThemeManifest struct{ Name, DisplayName, Description, Author, License, MinGoblogVersion, Homepage string } // json: name, display_name, description, author, license, min_goblog_version, homepage
  func ParseThemeManifest(b []byte) (ThemeManifest, error)
  func ParseArchive(zipBytes []byte) (map[string][]byte, error)   // keys like "templates/home.html", "static/css/a.css"
  func ContentHash(files map[string][]byte) string
  ```

- [ ] **Step 1: Write the failing tests**

```go
package registry

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

const goodThemeManifest = `{
  "name": "ocean",
  "display_name": "Ocean",
  "description": "Blue and calm.",
  "author": "Jason Ernst",
  "license": "MIT",
  "min_goblog_version": "0.5.0",
  "homepage": "https://example.test"
}`

// zipOf builds an archive with the given entries, optionally under a
// top-level folder the way GitHub's tag archives are laid out.
func zipOf(t *testing.T, prefix string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseThemeManifest(t *testing.T) {
	m, err := ParseThemeManifest([]byte(goodThemeManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "ocean" || m.DisplayName != "Ocean" || m.License != "MIT" || m.MinGoblogVersion != "0.5.0" || m.Homepage != "https://example.test" {
		t.Errorf("manifest = %+v", m)
	}
	cases := map[string]string{
		`{"name":"Ocean"}`:                                                        "name must match",
		strings.Replace(goodThemeManifest, `"ocean"`, `"default"`, 1):             "reserved",
		strings.Replace(goodThemeManifest, `"ocean"`, `"installed"`, 1):          "reserved",
		strings.Replace(goodThemeManifest, `"MIT"`, `"WTFPL"`, 1):                 "license",
		strings.Replace(goodThemeManifest, `"0.5.0"`, `"v0.5.0"`, 1):              "min_goblog_version",
		strings.Replace(goodThemeManifest, `"Blue and calm."`, `""`, 1):           "description is required",
		`not json`:                                                                "goblog-theme.json",
	}
	for in, want := range cases {
		if _, err := ParseThemeManifest([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseThemeManifest(%q): want error containing %q, got %v", in, want, err)
		}
	}
}

func TestParseArchive_StripsPrefixAndFilters(t *testing.T) {
	z := zipOf(t, "ocean-1.0.0/", map[string]string{
		"templates/home.html": "home", "static/css/a.css": "css", "README.md": "readme",
		"goblog-theme.json": goodThemeManifest, "templates/": "", ".github/workflows/x.yml": "ci",
	})
	files, err := ParseArchive(z)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || string(files["templates/home.html"]) != "home" || string(files["static/css/a.css"]) != "css" {
		t.Errorf("files = %v", keys(files))
	}
	// No top-level folder is fine too.
	flat, err := ParseArchive(zipOf(t, "", map[string]string{"templates/home.html": "h"}))
	if err != nil || len(flat) != 1 {
		t.Errorf("flat archive: %v %v", keys(flat), err)
	}
}

func TestParseArchive_Rejects(t *testing.T) {
	cases := map[string][]byte{
		"not a zip": []byte("nope"),
		"traversal": zipOf(t, "x/", map[string]string{"templates/../../etc/passwd": "p"}),
		"absolute":  zipOf(t, "", map[string]string{"/templates/home.html": "h"}),
	}
	for name, z := range cases {
		if _, err := ParseArchive(z); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// Too many entries.
	many := map[string]string{}
	for i := 0; i <= MaxArchiveEntries; i++ {
		many["static/"+strings.Repeat("a", i%10)+string(rune('a'+i%26))+strings.Repeat("b", i/26)] = "x"
	}
	if _, err := ParseArchive(zipOf(t, "", many)); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Errorf("entry cap: %v", err)
	}
	// Symlink entry.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "static/link"}
	hdr.SetMode(0o777 | 1<<31) // os.ModeSymlink is bit 31 in zip's external attrs mapping via SetMode
	f, _ := w.CreateHeader(hdr)
	f.Write([]byte("templates/home.html"))
	w.Close()
	if _, err := ParseArchive(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: %v", err)
	}
}

func TestContentHash(t *testing.T) {
	a := map[string][]byte{"templates/home.html": []byte("h"), "static/a.css": []byte("c")}
	b := map[string][]byte{"static/a.css": []byte("c"), "templates/home.html": []byte("h")}
	if ContentHash(a) != ContentHash(b) {
		t.Error("hash must not depend on map order")
	}
	c := map[string][]byte{"templates/home.html": []byte("h"), "static/a.css": []byte("d")}
	if ContentHash(a) == ContentHash(c) {
		t.Error("hash must change with content")
	}
	// Length is part of the input, so moving a byte across a boundary changes it.
	d := map[string][]byte{"templates/home.html": []byte("hc"), "static/a.css": []byte("")}
	if ContentHash(a) == ContentHash(d) {
		t.Error("hash must bind bytes to their file")
	}
	if len(ContentHash(a)) != 64 {
		t.Errorf("hex sha256 expected, got %q", ContentHash(a))
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

For the symlink case: `zip.FileHeader.SetMode(fs.ModeSymlink | 0o777)` is the intended call — import `io/fs` and use `hdr.SetMode(fs.ModeSymlink | 0o777)` instead of the bit-twiddling line above.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/registry/ -run 'Theme|Archive|ContentHash'`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write `theme.go`**

```go
package registry

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// Kinds of entry the directory publishes.
const (
	KindPlugin = "plugin"
	KindTheme  = "theme"
)

// MaxArchiveEntries bounds a theme archive; a theme is a few dozen files.
const MaxArchiveEntries = 2000

// MaxScreenshotBytes caps screenshot.png/.jpg. GitHub's contents API only
// returns files up to 1 MiB inline, so a larger screenshot could not be
// checked anyway.
const MaxScreenshotBytes = 1 << 20

// ThemeManifest is goblog-theme.json at the root of a theme repository.
type ThemeManifest struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Author           string `json:"author"`
	License          string `json:"license"`
	MinGoblogVersion string `json:"min_goblog_version"`
	Homepage         string `json:"homepage"`
}

// reservedThemeNames are directory names the loader treats specially or
// that ship with goblog; a directory theme may not claim them.
var reservedThemeNames = map[string]bool{"default": true, "minimal": true, "forest": true, "installed": true, "shared": true}

// ParseThemeManifest decodes and validates a theme manifest.
func ParseThemeManifest(b []byte) (ThemeManifest, error) {
	var m ThemeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return ThemeManifest{}, fmt.Errorf("goblog-theme.json: %w", err)
	}
	var problems []string
	if !NamePattern.MatchString(m.Name) {
		problems = append(problems, "name must match ^[a-z0-9-]+$")
	} else if reservedThemeNames[m.Name] {
		problems = append(problems, fmt.Sprintf("name %q is reserved", m.Name))
	}
	for _, f := range []struct{ field, v string }{{"display_name", m.DisplayName}, {"description", m.Description}, {"author", m.Author}} {
		if strings.TrimSpace(f.v) == "" {
			problems = append(problems, f.field+" is required")
		}
	}
	if !knownLicenses[m.License] {
		problems = append(problems, fmt.Sprintf("license %q is not a known SPDX identifier", m.License))
	}
	if !versionPattern.MatchString(m.MinGoblogVersion) {
		problems = append(problems, "min_goblog_version must be a plain semver like 0.5.0")
	}
	if len(problems) > 0 {
		return ThemeManifest{}, fmt.Errorf("goblog-theme.json: %s", strings.Join(problems, "; "))
	}
	return m, nil
}

// ParseArchive reads a theme archive (GitHub's tag zipball, or any zip with
// the same layout) into memory, keeping only templates/** and static/**.
// GitHub wraps everything in one "<repo>-<tag>/" folder, which is stripped
// when every entry shares it. Symlinks and paths that escape the archive
// are refused; the entry count and total size are bounded.
func ParseArchive(zipBytes []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if len(zr.File) > MaxArchiveEntries {
		return nil, fmt.Errorf("archive has %d entries; the limit is %d", len(zr.File), MaxArchiveEntries)
	}
	prefix := commonFolder(zr.File)
	files := map[string][]byte{}
	total := 0
	for _, f := range zr.File {
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive: %s is a symlink", f.Name)
		}
		if strings.HasSuffix(f.Name, "/") {
			continue // directory entry
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if strings.HasPrefix(f.Name, "/") || !fs.ValidPath(name) {
			return nil, fmt.Errorf("archive: unsafe path %q", f.Name)
		}
		if !strings.HasPrefix(name, "templates/") && !strings.HasPrefix(name, "static/") {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxAssetBytes) {
			return nil, fmt.Errorf("archive: %s is larger than %d bytes", f.Name, MaxAssetBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(MaxAssetBytes)+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", f.Name, err)
		}
		total += len(b)
		if total > MaxAssetBytes {
			return nil, fmt.Errorf("archive: extracted size exceeds %d bytes", MaxAssetBytes)
		}
		files[name] = b
	}
	return files, nil
}

// commonFolder returns "<folder>/" when every entry lives under the same
// top-level folder, else "".
func commonFolder(entries []*zip.File) string {
	if len(entries) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(entries[0].Name, "/")
	if !ok || first == "" {
		return ""
	}
	prefix := first + "/"
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, prefix) {
			return ""
		}
	}
	return prefix
}

// ContentHash is the index's sha256 for a theme: a hash of the extracted
// files rather than the archive bytes, because GitHub does not promise
// that a tag's zipball is byte-stable. Each file contributes its path, its
// length and its bytes, in path order.
func ContentHash(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		io.WriteString(h, p)
		h.Write([]byte{0})
		io.WriteString(h, strconv.Itoa(len(files[p])))
		h.Write([]byte{0})
		h.Write(files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./plugins/directory/registry/ -run 'Theme|Archive|ContentHash' -v`
Expected: PASS. If the traversal case passes through `fs.ValidPath` because zip normalises the name, assert on the returned map instead (the file must not appear) — but with `templates/../../etc/passwd` the `..` element makes `ValidPath` false, so it should error.

- [ ] **Step 5: Commit**

```bash
git add plugins/directory/registry && git commit -m "registry: theme manifest, archive parsing and content hash

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `registry` — `Zipball`/`FileURL`, `ValidateThemeEntry`, `BuildTheme`, `Kind`/`ScreenshotURL`

**Files:**
- Modify: `plugins/directory/registry/source.go`, `source_test.go`, `build.go`, `build_test.go`, `validate_test.go` (memSource)
- Modify: `plugins/directory/registry/theme.go`, `theme_test.go` (append)

**Interfaces:**
- Produces:
  ```go
  // Source gains:
  Zipball(ctx context.Context, owner, repo, ref string) ([]byte, error)
  FileURL(owner, repo, ref, path string) string
  // IndexEntry gains:
  Kind          string `json:"kind"`
  ScreenshotURL string `json:"screenshot_url,omitempty"`
  type ThemeValidator interface{ Validate(ctx context.Context, files map[string][]byte) error }
  type FakeThemeValidator struct{ Err error }
  type ValidatedTheme struct{ Repo, Owner, Name string; Manifest ThemeManifest; Release Release; Version string; Releases []Release; Files map[string][]byte; SHA256, ScreenshotURL string }
  func ValidateThemeEntry(ctx context.Context, src Source, tv ThemeValidator, repo string) (*ValidatedTheme, error)
  func BuildTheme(ctx context.Context, src Source, tv ThemeValidator, repo, baseURL string) (DetailDoc, error)
  ```
  `BuildRepo` now sets `Kind: KindPlugin`.

- [ ] **Step 1: Extend the fakes and write the tests**

`validate_test.go` — `memSource` gains:

```go
	zipballs map[string][]byte // "owner/repo@ref" → archive
```
and methods:
```go
func (m *memSource) Zipball(_ context.Context, owner, repo, ref string) ([]byte, error) {
	if b, ok := m.zipballs[owner+"/"+repo+"@"+ref]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%s/%s@%s: no archive", owner, repo, ref)
}

func (m *memSource) FileURL(owner, repo, ref, path string) string {
	return "https://raw.test/" + owner + "/" + repo + "/" + ref + "/" + path
}
```

`source_test.go` — in `fakeGitHub` add, after the asset handlers:

```go
	// Zipball: the API redirects to a storage host; the client follows it.
	mux.HandleFunc("GET /repos/o/r/zipball/v1.1.0", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/codeload/o/r/v1.1.0", http.StatusFound)
	})
	mux.HandleFunc("GET /codeload/o/r/v1.1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK\x03\x04zip"))
	})
```
and in `TestGitHubSource` append:
```go
	zb, err := src.Zipball(ctx, "o", "r", "v1.1.0")
	if err != nil || string(zb) != "PK\x03\x04zip" {
		t.Errorf("Zipball: %q %v", zb, err)
	}
	if _, err := src.Zipball(ctx, "o", "r", "v9.9.9"); err == nil {
		t.Error("Zipball of an unknown ref should fail")
	}
	if got := src.FileURL("o", "r", "v1.1.0", "screenshot.png"); got != "https://raw.githubusercontent.com/o/r/v1.1.0/screenshot.png" {
		t.Errorf("FileURL = %q", got)
	}
```

`theme_test.go` — append:

```go
func oceanSource(t *testing.T) *memSource {
	t.Helper()
	src := &memSource{
		releases: map[string][]Release{"o/ocean": {
			{Tag: "v1.0.0", Body: "First", URL: "https://github.com/o/ocean/releases/tag/v1.0.0", PublishedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)},
		}},
		files: map[string]string{
			"o/ocean@v1.0.0:goblog-theme.json": goodThemeManifest,
			"o/ocean@v1.0.0:README.md":         "# Ocean",
			"o/ocean@v1.0.0:screenshot.png":    "\x89PNG",
		},
		zipballs: map[string][]byte{"o/ocean@v1.0.0": zipOf(t, "ocean-1.0.0/", map[string]string{
			"templates/home.html": "home", "static/css/ocean.css": "css", "README.md": "# Ocean",
		})},
		stars: map[string]int{"o/ocean": 3},
	}
	return src
}

func TestValidateThemeEntry_Good(t *testing.T) {
	v, err := ValidateThemeEntry(context.Background(), oceanSource(t), &FakeThemeValidator{}, "o/ocean")
	if err != nil {
		t.Fatal(err)
	}
	if v.Manifest.Name != "ocean" || v.Version != "1.0.0" || len(v.Files) != 2 || v.ScreenshotURL != "https://raw.test/o/ocean/v1.0.0/screenshot.png" {
		t.Errorf("validated = %+v", v)
	}
	want := ContentHash(map[string][]byte{"templates/home.html": []byte("home"), "static/css/ocean.css": []byte("css")})
	if v.SHA256 != want {
		t.Errorf("sha256 = %s, want %s", v.SHA256, want)
	}
}

func TestValidateThemeEntry_Errors(t *testing.T) {
	cases := map[string]struct {
		mutate func(s *memSource, tv *FakeThemeValidator)
		want   string
	}{
		"missing manifest":   {func(s *memSource, _ *FakeThemeValidator) { delete(s.files, "o/ocean@v1.0.0:goblog-theme.json") }, "goblog-theme.json"},
		"bad manifest":       {func(s *memSource, _ *FakeThemeValidator) { s.files["o/ocean@v1.0.0:goblog-theme.json"] = `{"name":"Bad"}` }, "name must match"},
		"missing readme":     {func(s *memSource, _ *FakeThemeValidator) { delete(s.files, "o/ocean@v1.0.0:README.md") }, "README.md"},
		"missing screenshot": {func(s *memSource, _ *FakeThemeValidator) { delete(s.files, "o/ocean@v1.0.0:screenshot.png") }, "screenshot"},
		"jpg accepted":       {func(s *memSource, _ *FakeThemeValidator) { s.files["o/ocean@v1.0.0:screenshot.jpg"] = s.files["o/ocean@v1.0.0:screenshot.png"]; delete(s.files, "o/ocean@v1.0.0:screenshot.png") }, ""},
		"screenshot too big": {func(s *memSource, _ *FakeThemeValidator) { s.files["o/ocean@v1.0.0:screenshot.png"] = strings.Repeat("x", MaxScreenshotBytes+1) }, "1 MiB"},
		"no archive":         {func(s *memSource, _ *FakeThemeValidator) { delete(s.zipballs, "o/ocean@v1.0.0") }, "no archive"},
		"bad archive":        {func(s *memSource, _ *FakeThemeValidator) { s.zipballs["o/ocean@v1.0.0"] = []byte("nope") }, "archive"},
		"templates broken":   {func(_ *memSource, tv *FakeThemeValidator) { tv.Err = errors.New("templates/home.html: unexpected {{end}}") }, "do not load"},
	}
	for name, c := range cases {
		s, tv := oceanSource(t), &FakeThemeValidator{}
		c.mutate(s, tv)
		_, err := ValidateThemeEntry(context.Background(), s, tv, "o/ocean")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}

func TestBuildTheme(t *testing.T) {
	d, err := BuildTheme(context.Background(), oceanSource(t), &FakeThemeValidator{}, "o/ocean", "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	e := d.IndexEntry
	if e.Kind != KindTheme || e.InstallType != "theme" || e.Runtime != "" || e.AllowedHosts == nil || len(e.AllowedHosts) != 0 ||
		e.DownloadURL != "https://github.com/o/ocean/archive/refs/tags/v1.0.0.zip" ||
		e.DetailURL != "https://example.test/themes/ocean.json" || e.ScreenshotURL != "https://raw.test/o/ocean/v1.0.0/screenshot.png" ||
		e.Stars != 3 || e.Version != "1.0.0" || e.MinGoblogVersion != "0.5.0" || e.SourceURL != "https://github.com/o/ocean" {
		t.Errorf("entry = %+v", e)
	}
	if d.ReadmeHTML != "<p># Ocean</p>" || len(d.Releases) != 1 || d.Releases[0].NotesHTML != "<p>First</p>" {
		t.Errorf("docs = %+v", d)
	}
}
```

Add `"context"`, `"errors"`, `"time"` to `theme_test.go`'s imports. In `build_test.go`'s `TestBuildRepo_Good`, add `Kind: "plugin",` to `want` (the struct comparison is exact).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/registry/ 2>&1 | head -5`
Expected: compile errors (memSource does not implement Source until Step 3 lands, `Zipball` undefined…).

- [ ] **Step 3: Implement**

`source.go` — add to the `Source` interface:

```go
	// Zipball downloads the archive of ref (a tag) — at most MaxAssetBytes.
	Zipball(ctx context.Context, owner, repo, ref string) ([]byte, error)
	// FileURL is the public raw URL of path at ref (for hot-linked
	// screenshots); no request is made.
	FileURL(owner, repo, ref, path string) string
```

and the methods:

```go
func (g *GitHubSource) Zipball(ctx context.Context, owner, repo, ref string) ([]byte, error) {
	req, err := g.newRequest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/zipball/%s", owner, repo, ref), nil)
	if err != nil {
		return nil, err
	}
	// The API answers with a redirect to codeload.github.com; Go's client
	// follows it and drops the Authorization header across hosts.
	b, err := g.do(g.assetClient, req, MaxAssetBytes)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds") {
			return nil, fmt.Errorf("archive of %s/%s@%s exceeds %d bytes", owner, repo, ref, MaxAssetBytes)
		}
		return nil, fmt.Errorf("download archive of %s/%s@%s: %w", owner, repo, ref, err)
	}
	return b, nil
}

// FileURL points at raw.githubusercontent.com, which serves repository
// files publicly without API quota.
func (g *GitHubSource) FileURL(owner, repo, ref, path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, path)
}
```

`build.go` — `IndexEntry` gains, after `Stars`:

```go
	Kind          string `json:"kind"`                     // "plugin" or "theme"
	ScreenshotURL string `json:"screenshot_url,omitempty"` // themes only
```

`buildDetail` sets `Kind: KindPlugin` in its `entry` literal, and its README/changelog/releases rendering moves into a helper both builders call:

```go
// renderDocs fetches README.md and CHANGELOG.md at tag and renders them
// and the release notes through GitHub's markdown API.
func renderDocs(ctx context.Context, src Source, owner, name, tag string, all []Release) (readme, changelog template.HTML, releases []ReleaseDoc, err error) {
	ownerRepo := owner + "/" + name
	rb, err := src.File(ctx, owner, name, tag, "README.md")
	if err != nil {
		return "", "", nil, fmt.Errorf("README.md: %w", err)
	}
	readmeHTML, err := src.RenderMarkdown(ctx, ownerRepo, string(rb))
	if err != nil {
		return "", "", nil, err
	}
	if len(readmeHTML) > MaxRenderedBytes {
		return "", "", nil, fmt.Errorf("%s: rendered README.md is %d bytes; the limit is %d (1 MiB)", ownerRepo, len(readmeHTML), MaxRenderedBytes)
	}
	changelogHTML := ""
	if cl, err := src.File(ctx, owner, name, tag, "CHANGELOG.md"); err == nil {
		if changelogHTML, err = src.RenderMarkdown(ctx, ownerRepo, string(cl)); err != nil {
			return "", "", nil, err
		}
		if len(changelogHTML) > MaxRenderedBytes {
			return "", "", nil, fmt.Errorf("%s: rendered CHANGELOG.md is %d bytes; the limit is %d (1 MiB)", ownerRepo, len(changelogHTML), MaxRenderedBytes)
		}
	} else if !isNotFound(err) {
		return "", "", nil, fmt.Errorf("CHANGELOG.md: %w", err)
	}
	releases = make([]ReleaseDoc, 0, len(all))
	for _, r := range all {
		notes, err := src.RenderMarkdown(ctx, ownerRepo, r.Body)
		if err != nil {
			return "", "", nil, err
		}
		releases = append(releases, ReleaseDoc{Version: strings.TrimPrefix(r.Tag, "v"), ReleasedAt: r.PublishedAt.UTC().Format(time.RFC3339), NotesHTML: template.HTML(notes), URL: r.URL})
	}
	return template.HTML(readmeHTML), template.HTML(changelogHTML), releases, nil
}
```

(Keep the existing error strings exactly — the current tests match on them.) `buildDetail` then becomes: build `entry`, best-effort stars, `readme, changelog, releases, err := renderDocs(...)`, return `DetailDoc{IndexEntry: entry, ReadmeHTML: readme, ChangelogHTML: changelog, Releases: releases}`.

`theme.go` — append:

```go
// ThemeValidator checks a theme's files the way goblog would load them. The
// real one (theme.ValidateFiles, wrapped by the directory plugin) parses
// the templates on top of the shared and default sets; tests use
// FakeThemeValidator.
type ThemeValidator interface {
	Validate(ctx context.Context, files map[string][]byte) error
}

// FakeThemeValidator accepts everything unless Err is set.
type FakeThemeValidator struct{ Err error }

func (f *FakeThemeValidator) Validate(context.Context, map[string][]byte) error { return f.Err }

// ValidatedTheme is a theme repository that passed every check.
type ValidatedTheme struct {
	Repo, Owner, Name string
	Manifest          ThemeManifest
	Release           Release
	Version           string
	Releases          []Release
	Files             map[string][]byte // templates/** and static/**
	SHA256            string            // ContentHash(Files)
	ScreenshotURL     string
}

// ValidateThemeEntry checks a theme repository end to end: a vX.Y.Z
// release, manifest, README and screenshot at that tag, and an archive
// whose templates load.
func ValidateThemeEntry(ctx context.Context, src Source, tv ThemeValidator, repo string) (*ValidatedTheme, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%q: repo must be owner/name", repo)
	}
	latest, releases, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(latest.Tag, "v")
	at := repo + "@" + latest.Tag

	mb, err := src.File(ctx, owner, name, latest.Tag, "goblog-theme.json")
	if err != nil {
		return nil, fmt.Errorf("%s: goblog-theme.json: %w", at, err)
	}
	manifest, err := ParseThemeManifest(mb)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	if _, err := src.File(ctx, owner, name, latest.Tag, "README.md"); err != nil {
		return nil, fmt.Errorf("%s: README.md: %w", at, err)
	}
	shot := ""
	for _, candidate := range []string{"screenshot.png", "screenshot.jpg"} {
		b, err := src.File(ctx, owner, name, latest.Tag, candidate)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %v (it must be at most 1 MiB)", at, candidate, err)
		}
		if len(b) > MaxScreenshotBytes {
			return nil, fmt.Errorf("%s: %s is %d bytes; the limit is %d (1 MiB)", at, candidate, len(b), MaxScreenshotBytes)
		}
		shot = candidate
		break
	}
	if shot == "" {
		return nil, fmt.Errorf("%s: screenshot.png (or screenshot.jpg) is required", at)
	}
	zb, err := src.Zipball(ctx, owner, name, latest.Tag)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	files, err := ParseArchive(zb)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	if err := tv.Validate(ctx, files); err != nil {
		return nil, fmt.Errorf("%s: templates do not load: %w", at, err)
	}
	return &ValidatedTheme{
		Repo: repo, Owner: owner, Name: name, Manifest: manifest, Release: latest, Version: version, Releases: releases,
		Files: files, SHA256: ContentHash(files), ScreenshotURL: src.FileURL(owner, name, latest.Tag, shot),
	}, nil
}

// BuildTheme validates repo and returns the document the directory
// publishes for it under /themes.
func BuildTheme(ctx context.Context, src Source, tv ThemeValidator, repo, baseURL string) (DetailDoc, error) {
	v, err := ValidateThemeEntry(ctx, src, tv, repo)
	if err != nil {
		return DetailDoc{}, err
	}
	ownerRepo := v.Owner + "/" + v.Name
	entry := IndexEntry{
		Kind:             KindTheme,
		Name:             v.Manifest.Name,
		DisplayName:      v.Manifest.DisplayName,
		Description:      v.Manifest.Description,
		Version:          v.Version,
		Author:           v.Manifest.Author,
		License:          v.Manifest.License,
		SourceURL:        "https://github.com/" + ownerRepo,
		DownloadURL:      fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.zip", ownerRepo, v.Release.Tag),
		SHA256:           v.SHA256,
		MinGoblogVersion: v.Manifest.MinGoblogVersion,
		InstallType:      "theme",
		AllowedHosts:     []string{},
		ReleasedAt:       v.Release.PublishedAt.UTC().Format(time.RFC3339),
		DetailURL:        strings.TrimSuffix(baseURL, "/") + "/themes/" + v.Manifest.Name + ".json",
		ScreenshotURL:    v.ScreenshotURL,
	}
	if stars, err := src.RepoStars(ctx, v.Owner, v.Name); err != nil {
		log.Printf("%s: stars unavailable, using 0: %v", ownerRepo, err)
	} else {
		entry.Stars = stars
	}
	readme, changelog, releases, err := renderDocs(ctx, src, v.Owner, v.Name, v.Release.Tag, v.Releases)
	if err != nil {
		return DetailDoc{}, err
	}
	return DetailDoc{IndexEntry: entry, ReadmeHTML: readme, ChangelogHTML: changelog, Releases: releases}, nil
}
```

(add `"context"`, `"log"`, `"time"` to `theme.go`'s imports.)

- [ ] **Step 4: Run the package**

Run: `go test ./plugins/directory/registry/ -v 2>&1 | grep -E '^(--- |ok|FAIL)' | head -60`
Expected: all PASS, including the untouched plugin tests. Also `go build ./...` — it will fail in `plugins/directory` and `admin` because their fakes lack the new methods; that is Task 6/8 work, but to keep the tree building add the two methods now to `fakeSource` in `plugins/directory/service_test.go` and `stubSource` in `admin/directory_test.go` (return `nil, errors.New("no archive")` and `"https://raw.test/..."` respectively) so `go test ./...` stays green.

- [ ] **Step 5: Commit**

```bash
git add plugins/directory admin && git commit -m "registry: validate and build theme repositories

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Directory store and service become kind-aware

**Files:**
- Modify: `plugins/directory/store.go`, `store_test.go`, `service.go`, `service_test.go`, `directory.go` (only `NewService` call), `admin/directory_test.go` + `admin/directory.go` (only the changed `Submit`/`Add`/`Index`/`Detail` call sites — the API shape changes in Task 8)

**Interfaces:**
- Consumes: `registry.KindPlugin/KindTheme`, `registry.BuildTheme`, `registry.ThemeValidator`, `registry.FakeThemeValidator`.
- Produces:
  ```go
  const ( KindPlugin = registry.KindPlugin; KindTheme = registry.KindTheme )
  var ErrBadKind = errors.New("kind must be plugin or theme")
  // Repo.Kind, Build.Kind (string); Build unique index is (kind, name)
  func Migrate(db *gorm.DB) error                     // also backfills kind = plugin and drops the old name-only index
  func NewService(db *gorm.DB, newSource func(token string) registry.Source, val registry.Validator, tv registry.ThemeValidator, siteURL func() string) *Service
  func (s *Service) Submit(ctx context.Context, kind, input, ip, token string) (*Repo, error)
  func (s *Service) Add(ctx context.Context, kind, input, token string) (*Repo, error)
  func (s *Service) Index(kind string) ([]byte, []registry.IndexEntry)
  func (s *Service) Detail(kind, name string) (registry.DetailDoc, bool)
  // RepoView gains Kind string `json:"kind"` and ScreenshotURL string `json:"screenshot_url,omitempty"`
  ```

- [ ] **Step 1: Update and extend the tests**

`store_test.go`: `seed` takes a kind — change its signature to `seed(t, db, kind, repo, status string, d registry.DetailDoc) Repo` and set `r.Kind = kind`, `b.Kind = kind`; update every existing call to pass `KindPlugin`. `doc()` sets `Kind: KindPlugin` on the entry. Replace `TestApprovedDocsAndEncodeIndex`'s body so it seeds a theme too and asserts `approvedDocs(db, KindPlugin)` returns only plugins and `approvedDocs(db, KindTheme)` only the theme. Add:

```go
func TestMigrate_BackfillsKindAndDropsNameIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-kind schema: build the tables from a model without
	// Kind and with the old name-only unique index.
	type oldBuild struct {
		ID     uint   `gorm:"primaryKey"`
		RepoID uint   `gorm:"uniqueIndex"`
		Name   string `gorm:"uniqueIndex;size:128"`
		Doc    string
	}
	type oldRepo struct {
		ID   uint   `gorm:"primaryKey"`
		Repo string `gorm:"uniqueIndex;size:255"`
	}
	if err := db.Table("directory_repos").AutoMigrate(&oldRepo{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("directory_builds").AutoMigrate(&oldBuild{}); err != nil {
		t.Fatal(err)
	}
	db.Table("directory_repos").Create(&oldRepo{Repo: "o/hello"})
	db.Table("directory_builds").Create(&oldBuild{RepoID: 1, Name: "hello", Doc: "{}"})

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var r Repo
	db.First(&r)
	var b Build
	db.First(&b)
	if r.Kind != KindPlugin || b.Kind != KindPlugin {
		t.Errorf("existing rows must become plugins: %q %q", r.Kind, b.Kind)
	}
	// A theme may now share the name.
	theme := Repo{Repo: "o/hello-theme", Kind: KindTheme, Status: StatusApproved, SubmittedAt: time.Now()}
	db.Create(&theme)
	tb := Build{RepoID: theme.ID, Kind: KindTheme}
	tb.SetDoc(doc("hello", "1.0.0", 0))
	if err := db.Create(&tb).Error; err != nil {
		t.Errorf("a theme and a plugin may share a name: %v", err)
	}
	// But two of the same kind may not.
	dup := Build{RepoID: 99, Kind: KindPlugin}
	dup.SetDoc(doc("hello", "1.0.0", 0))
	if err := db.Create(&dup).Error; err == nil {
		t.Error("(kind, name) must be unique")
	}
}
```

`service_test.go`: `fakeRepo` gains `theme bool` and `files map[string]string` (archive contents); `fakeSource.File` serves `goblog-theme.json` (name/display_name from the repo, license MIT, `min_goblog_version` `0.5.0`) and `screenshot.png` (`"\x89PNG"`) when `r.theme`; `fakeSource.Zipball` returns `zipOf`-style bytes built with `archive/zip` from `r.files` under `"<repo>-<version>/"` (copy the small helper from the registry tests into this file — it is test code in another package); `fakeSource.FileURL` returns `"https://raw.test/" + owner + "/" + repo + "/" + ref + "/" + path`. `newFixture` adds `"o/ocean": {theme: true, version: "1.0.0", name: "ocean", stars: 3, readme: "# Ocean", files: {"templates/home.html": "home"}}` and passes `&registry.FakeThemeValidator{}` to `NewService`. Every existing `Submit`/`Add`/`Index`/`Detail` call gains `KindPlugin`; `indexNames` takes a kind. Add:

```go
func TestService_ThemesAreSeparateFromPlugins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Add(ctx, "bogus", "o/hello", ""); !errors.Is(err, ErrBadKind) {
		t.Errorf("bad kind: %v", err)
	}
	if _, err := f.svc.Add(ctx, KindPlugin, "o/hello", ""); err != nil {
		t.Fatal(err)
	}
	// A theme repo whose manifest name is "hello" too.
	f.src.repos["o/ocean"].name = "hello"
	th, err := f.svc.Add(ctx, KindTheme, "o/ocean", "")
	if err != nil || th.Kind != KindTheme {
		t.Fatalf("add theme: %+v %v", th, err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 1 || names[0] != "hello" {
		t.Errorf("plugin index = %v", names)
	}
	if names := f.indexNames(t, KindTheme); len(names) != 1 || names[0] != "hello" {
		t.Errorf("theme index = %v", names)
	}
	d, ok := f.svc.Detail(KindTheme, "hello")
	if !ok || d.Kind != KindTheme || d.InstallType != "theme" || d.ScreenshotURL == "" {
		t.Errorf("theme detail = %+v %v", d, ok)
	}
	if d, _ := f.svc.Detail(KindPlugin, "hello"); d.Kind != KindPlugin {
		t.Errorf("plugin detail = %+v", d)
	}
	// Same kind, same name, different repo: refused.
	f.src.repos["o/zeta"].name = "hello"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "hello", Version: "0.1.0", Runtime: "wasm"}
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/zeta", "ip", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("same-kind collision: %v", err)
	}
	// A repo keeps its kind: resubmitting o/hello as a theme is a duplicate, not a new theme.
	if _, err := f.svc.Submit(ctx, KindTheme, "o/hello", "ip", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("repo already listed under another kind: %v", err)
	}
	views, _ := f.svc.List("")
	kinds := map[string]string{}
	for _, v := range views {
		kinds[v.Repo] = v.Kind
	}
	if kinds["o/hello"] != KindPlugin || kinds["o/ocean"] != KindTheme {
		t.Errorf("List kinds = %v", kinds)
	}
	if err := f.svc.RefreshAll(ctx, ""); err != nil {
		t.Errorf("refresh with both kinds: %v", err)
	}
}
```

`directory_test.go`/`submit_test.go`: pass `KindPlugin` where `Index`/`Detail`/`Submit`/`Add` are called (Task 7 rewrites the page-level tests, but the file must compile now). `admin/directory_test.go`: same for the two `Add`/`Index` uses; `stubSource` already has the new methods from Task 5.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/ 2>&1 | head`
Expected: compile errors (`KindPlugin`, new signatures).

- [ ] **Step 3: Implement**

`store.go`:

```go
// Kinds of entry the directory curates; aliases of the registry's.
const (
	KindPlugin = registry.KindPlugin
	KindTheme  = registry.KindTheme
)

// Repo gains:
	Kind string `gorm:"index;size:16" json:"kind"` // plugin or theme; a repo is one or the other

// Build: replace the Name line and add Kind so the two share one unique index:
	Kind string `gorm:"uniqueIndex:idx_directory_builds_kind_name;size:16"`
	Name string `gorm:"uniqueIndex:idx_directory_builds_kind_name;size:128"` // routes /plugins/<name> or /themes/<name>
```

```go
// Migrate creates or updates the directory's tables. Rows from before
// themes existed carry no kind and are plugins; the pre-kind unique index
// on name alone is dropped so a theme and a plugin may share a name.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Repo{}, &Build{}); err != nil {
		return err
	}
	for _, model := range []any{&Repo{}, &Build{}} {
		if err := db.Model(model).Where("kind = ? OR kind IS NULL", "").Update("kind", KindPlugin).Error; err != nil {
			return err
		}
	}
	m := db.Migrator()
	if m.HasIndex(&Build{}, "idx_directory_builds_name") {
		if err := m.DropIndex(&Build{}, "idx_directory_builds_name"); err != nil {
			return err
		}
	}
	return nil
}
```

`approvedDocs(db, kind)` adds `AND directory_builds.kind = ?` (`kind`) to its `Where`.

`service.go`:
- `var ErrBadKind = errors.New("kind must be plugin or theme")`; `func validKind(k string) bool { return k == KindPlugin || k == KindTheme }`.
- `Service` fields: `themeValidator registry.ThemeValidator`; the cache becomes `indexRaw map[string][]byte` and `indexEntries map[string][]registry.IndexEntry` (initialised in `NewService`).
- `NewService(db, newSource, val, tv, siteURL)` stores `tv`.
- `Submit(ctx, kind, input, ip, token)` / `Add(ctx, kind, input, token)`: first line `if !validKind(kind) { return nil, ErrBadKind }`; call `s.build(ctx, kind, repo, token)` and `s.save(kind, repo, doc, status, ip)`.
- `build(ctx, kind, repo, token)`:
  ```go
  	if kind == KindTheme {
  		return registry.BuildTheme(ctx, s.newSource(token), s.themeValidator, repo, s.siteURL())
  	}
  	return registry.BuildRepo(ctx, s.newSource(token), s.validator, repo, s.siteURL())
  ```
- `nameTaken(tx, kind, name, repo)`: `Where("directory_builds.kind = ? AND directory_builds.name = ? AND directory_repos.repo <> ?", kind, name, repo)`.
- `save(kind, repo, doc, status, ip)`: `nameTaken(tx, kind, doc.Name, repo)`; set `r.Kind = kind` alongside `r.Repo…`; set `b.Kind = kind` alongside `b.RepoID`.
- `rebuildRepo`: `s.build(ctx, r.Kind, r.Repo, token)`, `nameTaken(s.db, r.Kind, doc.Name, r.Repo)`.
- `Index(kind)`: read `s.indexRaw[kind]`; when nil, `regenerate()`; return `[]byte("[]\n"), nil` on error.
- `Detail(kind, name)`: add `AND directory_builds.kind = ?` (kind) to the query.
- `regenerate()`: loop over `[]string{KindPlugin, KindTheme}`, `approvedDocs(s.db, kind)` → `encodeIndex` → store under `kind` (all under one `s.mu.Lock()`).
- `view()`: `v.Kind = r.Kind`; from the doc also `v.ScreenshotURL = d.ScreenshotURL`. `RepoView` gains `Kind string json:"kind"` after `Status` and `ScreenshotURL string json:"screenshot_url,omitempty"` after `SourceURL`.
- `nameOf(repo)` unchanged.

`directory.go`: the `NewService` call passes `p.themeValidator` (a new field; Task 7 wires the real one — for now `New()` sets `themeValidator: &registry.FakeThemeValidator{}`? No: set it to `themeValidatorFunc(theme.ValidateFiles)` right away, see Task 7 for the adapter; if you want this task to compile on its own, define the adapter here:
```go
// fsThemeValidator checks a theme's files against this goblog's shared and
// default templates — the same rule the installer applies.
type fsThemeValidator struct{}

func (fsThemeValidator) Validate(_ context.Context, files map[string][]byte) error {
	return theme.ValidateFiles(files)
}
```
and `New()` sets `themeValidator: fsThemeValidator{}` (import `"goblog/theme"`). `SetThemeValidator(v registry.ThemeValidator)` next to `SetValidator`. `RenderPage`'s `p.svc.Index()`/`Detail()` calls pass `KindPlugin` for now; `renderSubmit`'s `Submit` call too.

`admin/directory.go`: `svc.Add(c.Request.Context(), directory.KindPlugin, req.Repo, …)` for now (Task 8 reads the kind from the body).

- [ ] **Step 4: Run everything**

Run: `gofmt -l plugins admin; go vet ./plugins/... ./admin/; go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add plugins/directory admin && git commit -m "directory: curate plugins and themes side by side (kind column, per-kind index)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: `/themes` pages — second page type, templates, submit by kind

**Files:**
- Modify: `plugins/directory/directory.go`, `directory_test.go`, `submit.go`, `submit_test.go`, `render.go`, `templates/submit.html`
- Create: `plugins/directory/templates/themes-listing.html`, `plugins/directory/templates/themes-detail.html`

**Interfaces:**
- Produces: `const ThemePageType = "theme-directory"`; `Pages()` returns both definitions; `func (p *Plugin) SetThemeValidator(v registry.ThemeValidator)`; `submitView.Kind`.

- [ ] **Step 1: Tests**

In `directory_test.go`:
- `TestPlugin_Identity`: assert `len(pages) == 2`, `pages[1].PageType == ThemePageType && pages[1].Slug == "themes" && pages[1].NavOrder == 31`.
- `TestOnInit_MigratesCreatesPageAndService`: assert a `theme-directory` page row exists too (`slug == "themes"`, `Title == "Themes"`).
- `newPluginFixture`: `p.SetThemeValidator(f.val2)` where the fixture has `&registry.FakeThemeValidator{}` (add a field `tv` to `fixture`).
- Add:

```go
func TestRenderPage_ThemesListingDetailAndIndex(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.svc.Add(context.Background(), KindTheme, "o/ocean", "")

	ctx, _ := newRenderCtx(t, http.MethodGet, "/themes", "", nil)
	tmpl, data := f.p.RenderPage(ctx, ThemePageType)
	html := content(t, data)
	if tmpl != "page_content.html" {
		t.Fatalf("tmpl = %q", tmpl)
	}
	for _, want := range []string{`href="/themes/ocean"`, `src="https://raw.test/o/ocean/v1.0.0/screenshot.png"`, "OCEAN", "v1.0.0", "★ 3", `href="/themes/submit"`, `href="/themes/index.json"`} {
		if !strings.Contains(html, want) {
			t.Errorf("themes listing missing %q in:\n%s", want, html)
		}
	}
	if strings.Contains(html, "/themes/hello") || strings.Contains(html, "HELLO") {
		t.Error("plugins must not appear in the theme listing")
	}

	ctx, w := newRenderCtx(t, http.MethodGet, "/themes/index.json", "index.json", nil)
	f.p.RenderPage(ctx, ThemePageType)
	if !strings.Contains(w.Body.String(), `"kind": "theme"`) || strings.Contains(w.Body.String(), `"name": "hello"`) {
		t.Errorf("themes index = %s", w.Body.String())
	}

	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes/ocean", "ocean", nil)
	_, data = f.p.RenderPage(ctx, ThemePageType)
	html = content(t, data)
	for _, want := range []string{`src="https://raw.test/o/ocean/v1.0.0/screenshot.png"`, "<p># Ocean</p>", "0.5.0", `href="https://github.com/o/ocean/archive/refs/tags/v1.0.0.zip"`, "content hash"} {
		if !strings.Contains(html, want) {
			t.Errorf("theme detail missing %q in:\n%s", want, html)
		}
	}
	if data["title"] != "OCEAN" {
		t.Errorf("title = %v", data["title"])
	}

	// Kinds never cross pages.
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins/ocean", "ocean", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
		t.Error("/plugins/<theme name> must 404")
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes/hello", "hello", nil)
	if tmpl, _ := f.p.RenderPage(ctx, ThemePageType); tmpl != "" {
		t.Error("/themes/<plugin name> must 404")
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes", "", nil)
	if tmpl, _ := f.p.RenderPage(ctx, "other"); tmpl != "" {
		t.Error("unknown page types are declined")
	}
}
```

In `submit_test.go`: `TestRenderPage_SubmitForm` gets a theme variant — `GET /themes/submit` with `ThemePageType` renders `action="/themes/submit"`, mentions `goblog-theme.json` and `THEME_CONTRACT.md`, and not `goblog-plugin.json`. `TestRenderPage_SubmitPost` gets one more step: `POST /themes/submit` with `repo=o/ocean` → "Queued for review" and the row's `Kind == KindTheme`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/ -run 'Themes|Identity|OnInit|Submit' 2>&1 | head`
Expected: FAIL/compile errors.

- [ ] **Step 3: Implement**

`directory.go`:

```go
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
```

`Pages()` returns two entries: the existing one and

```go
		{
			PageType:    ThemePageType,
			Title:       "Themes",
			Slug:        "themes",
			ShowInNav:   true,
			NavOrder:    31,
			Description: "Browsable directory of goblog themes",
		},
```

`OnInit`: after creating the service, loop `for _, def := range p.Pages() { if err := ensurePage(db, def); err != nil { return err } }` where `ensurePage(db, def gplugin.PageDefinition) error` is the existing page-row logic extracted (query by `page_type`; slug collision → log and return nil; else create).

`SetThemeValidator`:
```go
// SetThemeValidator replaces the theme validator (tests).
func (p *Plugin) SetThemeValidator(v registry.ThemeValidator) { p.themeValidator = v }
```
(`OnInit` creates the service with `p.themeValidator`, so tests must call it before `OnInit`.)

`RenderPage`: replace the guard with

```go
	kind := kindOf(pageType)
	if kind == "" || p.svc == nil {
		return "", nil
	}
```
then: listing → `p.svc.Index(kind)` and `html, err := renderListingFor(kind, base, sorted)`; `index.json` → `p.svc.Index(kind)`; `submit` → `p.renderSubmit(ctx, base, kind)`; `<name>.json` and `<name>` → `p.svc.Detail(kind, name)`, detail page → `renderDetailFor(kind, base, d)`.

`render.go`:

```go
func renderListingFor(kind, base string, entries []Entry) (string, error) {
	name := "listing.html"
	if kind == KindTheme {
		name = "themes-listing.html"
	}
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, name, map[string]any{"Base": base, "Entries": entries})
	return buf.String(), err
}

func renderDetailFor(kind, base string, d Detail) (string, error) {
	name := "detail.html"
	if kind == KindTheme {
		name = "themes-detail.html"
	}
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, name, map[string]any{"Base": base, "Entry": d.IndexEntry, "Detail": &d, "Notice": ""})
	return buf.String(), err
}
```
(remove the old `renderListing`/`renderDetail` if nothing else uses them.)

`templates/themes-listing.html`:

```html
{{ if .Entries }}
<p>{{ len .Entries }} theme{{ if ne (len .Entries) 1 }}s{{ end }} in the directory, most-starred first.
Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.
Install them from your own goblog under <strong>Admin → Themes</strong>.</p>
<div class="row g-4">
{{ range .Entries }}
  <div class="col-md-6 col-lg-4">
    <div class="card h-100">
      {{ if .ScreenshotURL }}<a href="{{ $.Base }}/{{ .Name }}"><img src="{{ .ScreenshotURL }}" class="card-img-top" alt="Screenshot of {{ .DisplayName }}" loading="lazy"></a>{{ end }}
      <div class="card-body">
        <h2 class="h5 card-title"><a href="{{ $.Base }}/{{ .Name }}">{{ .DisplayName }}</a> <small class="text-muted">v{{ .Version }}</small></h2>
        <p class="card-text">{{ .Description }}</p>
        <p class="small text-muted mb-0">by {{ .Author }} · {{ .License }} · ★ {{ .Stars }} · <a href="{{ .SourceURL }}">source</a></p>
      </div>
    </div>
  </div>
{{ end }}
</div>
{{ else }}
<p>The theme directory is empty. Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.</p>
{{ end }}

<p class="mt-4">Made a theme? <a href="{{ .Base }}/submit">Submit it to the directory</a>.</p>
```

`templates/themes-detail.html`:

```html
<p><a href="{{ .Base }}">&larr; All themes</a></p>
<h2>{{ .Entry.DisplayName }} <small>v{{ .Entry.Version }}</small></h2>
<p>{{ .Entry.Description }}</p>
{{ if .Entry.ScreenshotURL }}<p><img src="{{ .Entry.ScreenshotURL }}" class="img-fluid rounded border" alt="Screenshot of {{ .Entry.DisplayName }}"></p>{{ end }}
<dl>
  <dt>Version</dt><dd>{{ .Entry.Version }}{{ if .Entry.ReleasedAt }} <small>(released {{ date .Entry.ReleasedAt }})</small>{{ end }}</dd>
  <dt>Author</dt><dd>{{ .Entry.Author }}</dd>
  <dt>License</dt><dd>{{ .Entry.License }}</dd>
  <dt>Source</dt><dd><a href="{{ .Entry.SourceURL }}">{{ .Entry.SourceURL }}</a></dd>
  <dt>Requires</dt><dd>goblog {{ .Entry.MinGoblogVersion }} or newer</dd>
  <dt>Download</dt><dd><a href="{{ .Entry.DownloadURL }}">{{ .Entry.DownloadURL }}</a><br><small>content hash <code>{{ .Entry.SHA256 }}</code></small></dd>
</dl>
<p>Install it from your own goblog under <strong>Admin → Themes</strong>.</p>
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
```

`submit.go`: `submitView` gains `Kind string`; `renderSubmit(ctx, base, kind)` passes `kind` into every `submitView` it builds, calls `p.svc.Submit(c.Request.Context(), kind, repo, …)`, and on `ErrAlreadyListed` uses `p.svc.nameOf(key)` as before. The page title is "Submit a plugin" / "Submit a theme" by kind.

`templates/submit.html`: the intro paragraph becomes

```html
{{ if eq .Kind "theme" }}
<p>A theme is a GitHub repository with a <code>goblog-theme.json</code> manifest, a <code>templates/</code> folder (only the templates you change — everything else comes from goblog's default theme), an optional <code>static/</code> folder, a <code>README.md</code> and a <code>screenshot.png</code>, released as <code>vX.Y.Z</code> tags — see the <a href="https://github.com/goblogplatform/goblog/blob/main/docs/THEME_CONTRACT.md">theme contract</a>. Paste the repository URL; it is checked right away and queued for a maintainer to approve.</p>
{{ else }}
<p>A plugin is a GitHub repository … (existing text) …</p>
{{ end }}
```
and the closing note reads "Validation downloads the latest release's {{ if eq .Kind "theme" }}archive and parses its templates against goblog's{{ else }}module, loads it in goblog's sandbox and checks that its name and version match the manifest and the release tag{{ end }}. Nothing is stored unless it passes."

- [ ] **Step 4: Run the package**

Run: `gofmt -l plugins/directory; go test ./plugins/directory/... -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: all ok.

- [ ] **Step 5: Commit**

```bash
git add plugins/directory && git commit -m "directory: /themes listing, detail and submission pages

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Admin Directory tab knows kinds; contract doc; spec patch; open PR 2

**Files:**
- Modify: `admin/directory.go`, `admin/directory_test.go`, `themes/default/templates/admin_plugins.html`, `docs/superpowers/specs/2026-09-20-theme-directory-design.md`
- Create: `docs/THEME_CONTRACT.md`

- [ ] **Step 1: API — `kind` on Add**

`AddDirectoryRepo` binds `{repo, kind}`; empty `kind` → `directory.KindPlugin`; `svc.Add(ctx, kind, repo, token)`; `ErrBadKind` → 400 (add to `directoryStatus`). Test: in `TestDirectoryAPI_Lifecycle` add `POST {repo:"o/hello", kind:"bogus"}` → 400 and, with the stub extended to serve a theme repo `o/ocean` (manifest `goblog-theme.json`, `screenshot.png`, a zip with one template), `POST {repo:"o/ocean", kind:"theme"}` → 200 with `"kind":"theme"`, then `GET /api/v1/directory/repos?status=approved` lists both kinds and the theme row carries `screenshot_url`.

- [ ] **Step 2: Template**

In the Directory tab's JS (`admin_plugins.html`):
- `pendingCard(r)`: prepend `<span class="badge text-bg-secondary">' + esc(r.kind) + '</span> ` to the title; when `r.kind === "theme"` and `httpURL(r.screenshot_url)`, insert `'<img src="' + esc(httpURL(r.screenshot_url)) + '" class="card-img-top" alt="">'` before `card-body`; hide the "Talks to" line for themes.
- `approvedRow(r)` / `rejectedRow(r)`: a kind badge after the display name / repo.
- The add form: two radios `name="directory-add-kind"` (`plugin` checked, `theme`) between the input and the button; the submit handler posts `{ repo, kind }`.
- The pane intro: "Repositories submitted at /plugins/submit and /themes/submit wait here…".

- [ ] **Step 3: `docs/THEME_CONTRACT.md`**

```markdown
# Publishing a goblog theme

The directory at [goblog.live/themes](https://goblog.live/themes) lists themes submitted at [goblog.live/themes/submit](https://goblog.live/themes/submit) and approved by a maintainer. A theme is a GitHub repository; each `vX.Y.Z` tag is a version. Submission checks the repository right away and queues it; once approved, the directory re-checks it every few hours and picks up new releases on its own.

## What the repository must contain

At the root, at the release tag:

| File | Required | Notes |
|---|---|---|
| `goblog-theme.json` | yes | the manifest, below |
| `templates/*.html` | yes (≥ 1) | only the templates you change; goblog loads them on top of its `default` theme, so everything you don't ship renders from default |
| `static/` | no | CSS/images, served at `/theme/…`; files you don't ship fall back to default's |
| `README.md` | yes | shown on the theme's directory page |
| `screenshot.png` or `screenshot.jpg` | yes | ≤ 1 MiB, shown in the listing (hot-linked from the tag) |
| `CHANGELOG.md` | no | shown when present |

No build step and no release workflow: the directory downloads GitHub's archive of the tag. Only `templates/` and `static/` are installed; the archive must be ≤ 16 MiB, ≤ 2000 entries, with no symlinks.

### `goblog-theme.json`

```json
{
  "name": "ocean",
  "display_name": "Ocean",
  "description": "One sentence shown in the listing.",
  "author": "Your Name",
  "license": "MIT",
  "min_goblog_version": "0.5.0",
  "homepage": "https://example.com/optional"
}
```

- `name`: `^[a-z0-9-]+$`, unique among themes, becomes the directory name under `themes/installed/` and the value of the `theme` setting. Not `default`, `minimal`, `forest`, `installed` or `shared`.
- `license`: an SPDX identifier from the list in `plugins/directory/registry/manifest.go`.
- `min_goblog_version`: plain semver; themes need at least `0.5.0` (the first goblog that layers themes on default).

### Templates

Start from `themes/default/templates` in the goblog repository at the version you target and copy only the files you want to change. Templates are Go `html/template`; the `rawHTML` function and everything in `templates/shared` are available. Each file must parse on its own — the directory checks that.

### Releases

- Tag releases `vX.Y.Z`; drafts and pre-releases are ignored.
- The release body is shown as the version's notes.
- The index's `sha256` is a hash of the extracted `templates/` and `static/` files, not of the zip, so GitHub re-compressing an archive does not break installs.
```

- [ ] **Step 4: Spec patch**

In the spec, §1: "`screenshot.png` or `screenshot.jpg`, ≤ 2 MiB" → "≤ 1 MiB (GitHub's contents API returns files up to 1 MiB inline)". §2 `MaxScreenshotBytes` accordingly if mentioned.

- [ ] **Step 5: Verify, commit, PR**

Run: `gofmt -l admin plugins; go vet ./admin/ ./plugins/...; go test ./...` → green.

```bash
git add admin themes/default/templates/admin_plugins.html docs && git commit -m "Admin directory tab curates themes; theme contract doc

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/theme-directory
gh pr create --base main --title "Theme directory: /themes pages, registry theme builder, kind-aware curation" --body "$(cat <<'BODY'
Part 2 of #560 (spec `docs/superpowers/specs/2026-09-20-theme-directory-design.md`). Stacked on #<PR1 number> — rebase after it merges.

- `registry`: `goblog-theme.json`, GitHub tag zipball parsing (templates/ + static/, traversal/symlink/size caps), content hash, `ValidateThemeEntry`/`BuildTheme`; `IndexEntry` gains `kind` and `screenshot_url`.
- `directory`: `kind` on repos and builds (backfilled to `plugin`), unique `(kind, name)`, per-kind index cache; `/themes`, `/themes/<name>`, `/themes/<name>.json`, `/themes/index.json`, `/themes/submit`.
- Admin → Plugins → Directory: kind badges, Plugin/Theme radio on Add, screenshots on pending theme cards.
- `docs/THEME_CONTRACT.md`.

`/plugins/index.json` is unchanged for existing installers.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

## PR 3 — theme installer and Admin → Themes

Branch `feat/theme-installer` from `feat/theme-directory` (rebase onto `main` once PR 2 merges).

### Task 9: `theme/installer` package

**Files:**
- Create: `theme/installer/installer.go`, `theme/installer/installer_test.go`

**Interfaces:**
- Consumes: `installer.Fetcher` (`plugin/installer`: `NewFetcher`, `Refresh`, `Ensure`, `Index`, `Entry`, `FetchedAt`), `directory.Entry`, `registry.ParseArchive`, `registry.ContentHash`, `theme.Dir/List/IsBuiltin/ValidName/ValidateFiles/InstalledRoot`.
- Produces:
  ```go
  package installer // import "goblog/theme/installer"
  const DefaultIndexURL = "https://www.goblog.live/themes/index.json"
  var ErrNotFound, ErrIncompatible, ErrChecksum, ErrAlreadyInstalled, ErrNotInstalled, ErrBuiltin, ErrActive, ErrLoad, ErrDirectoryUnavailable, ErrUpToDate, ErrDownload, ErrWrite, ErrNotTheme error
  type Installer struct{ Dir string; Directory *pinstaller.Fetcher; Version string; Client *http.Client; IndexURL func() string; ActiveTheme func() string; Activate func(name string) error }
  type Installed struct{ Name, DisplayName, Version string; Builtin, Active, UpdateAvailable bool; LatestVersion string }
  type Available struct{ directory.Entry; Compatible bool; Reason string }
  type Status struct{ Installed []Installed; Available []Available; Active, DirectoryURL, IndexFetchedAt, IndexError string; DirWritable bool; DirError string }
  type Result struct{ Name, Version, Message string }
  func (i *Installer) Status() Status
  func (i *Installer) Refresh() error
  func (i *Installer) Install(ctx context.Context, name string) (Result, error)
  func (i *Installer) Update(ctx context.Context, name string) (Result, error)
  func (i *Installer) Uninstall(name string) error
  func (i *Installer) ActivateTheme(name string) error
  ```
  JSON tags follow the plugin installer (`display_name`, `update_available`, `latest_version`, `directory_url`, `index_fetched_at`, `index_error`, `dir_writable`, `dir_error`).

- [ ] **Step 1: Write the failing tests**

```go
package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"
	"goblog/theme"

	pinstaller "goblog/plugin/installer"
)

func zipOf(t *testing.T, prefix string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

type harness struct {
	inst    *Installer
	srv     *httptest.Server
	index   []directory.Entry
	zips    map[string][]byte // path → archive
	active  string
	activations []string
	root    string
}

// newHarness serves an index with "ocean" v1.0.0 (a valid theme) and
// "broken" (a theme whose template does not parse), points the theme
// roots at temp dirs with a default theme, and wires an Installer.
func newHarness(t *testing.T) *harness {
	t.Helper()
	builtin, installed := t.TempDir(), t.TempDir()
	old := theme.BuiltinRoot
	theme.BuiltinRoot = builtin
	t.Cleanup(func() { theme.BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	shared := t.TempDir()
	oldShared := theme.SharedDir
	theme.SharedDir = shared
	t.Cleanup(func() { theme.SharedDir = oldShared })
	os.WriteFile(filepath.Join(shared, "shared.html"), []byte(`{{ define "shared" }}S{{ end }}`), 0o644)
	os.MkdirAll(filepath.Join(builtin, "default", "templates"), 0o755)
	os.WriteFile(filepath.Join(builtin, "default", "templates", "home.html"), []byte("default"), 0o644)
	os.MkdirAll(filepath.Join(builtin, "forest", "templates"), 0o755)

	h := &harness{active: "default", root: installed}
	oceanFiles := map[string][]byte{"templates/home.html": []byte("ocean"), "static/css/o.css": []byte("c")}
	h.zips = map[string][]byte{
		"/o/ocean/archive/refs/tags/v1.0.0.zip":  zipOf(t, "ocean-1.0.0/", map[string]string{"templates/home.html": "ocean", "static/css/o.css": "c", "README.md": "r"}),
		"/o/ocean/archive/refs/tags/v1.1.0.zip":  zipOf(t, "ocean-1.1.0/", map[string]string{"templates/home.html": "ocean 2"}),
		"/o/broken/archive/refs/tags/v1.0.0.zip": zipOf(t, "broken-1.0.0/", map[string]string{"templates/home.html": "{{ if }}"}),
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/themes/index.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(h.index)
			return
		}
		if z, ok := h.zips[r.URL.Path]; ok {
			w.Write(z)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.srv.Close)
	entry := func(name, version string, files map[string][]byte, min string) directory.Entry {
		return directory.Entry{Kind: registry.KindTheme, Name: name, DisplayName: strings.ToUpper(name), Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: h.srv.URL + "/o/" + name + "/archive/refs/tags/v" + version + ".zip",
			SHA256: registry.ContentHash(files), MinGoblogVersion: min, InstallType: "theme", AllowedHosts: []string{}, Stars: 1}
	}
	h.index = []directory.Entry{
		entry("ocean", "1.0.0", oceanFiles, "0.5.0"),
		entry("broken", "1.0.0", map[string][]byte{"templates/home.html": []byte("{{ if }}")}, "0.5.0"),
		entry("future", "1.0.0", oceanFiles, "9.0.0"),
		{Kind: registry.KindPlugin, Name: "hello", Version: "1.0.0", InstallType: "wasm", DownloadURL: h.srv.URL + "/x"},
	}
	h.inst = &Installer{
		Dir: installed, Directory: pinstaller.NewFetcher(h.srv.Client()), Version: "v0.5.0", Client: h.srv.Client(),
		IndexURL:    func() string { return h.srv.URL + "/themes/index.json" },
		ActiveTheme: func() string { return h.active },
		Activate:    func(name string) error { h.activations = append(h.activations, name); h.active = name; return nil },
	}
	return h
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	st := h.inst.Status()
	if st.Active != "default" || !st.DirWritable || st.IndexError != "" {
		t.Errorf("status = %+v", st)
	}
	names := map[string]Installed{}
	for _, i := range st.Installed {
		names[i.Name] = i
	}
	if !names["default"].Builtin || !names["default"].Active || !names["forest"].Builtin || len(names) != 2 {
		t.Errorf("installed = %+v", st.Installed)
	}
	avail := map[string]Available{}
	for _, a := range st.Available {
		avail[a.Name] = a
	}
	if len(avail) != 3 || !avail["ocean"].Compatible || avail["future"].Compatible || avail["future"].Reason == "" {
		t.Errorf("available = %+v", st.Available)
	}
	if _, ok := avail["hello"]; ok {
		t.Error("plugins in the index must be ignored")
	}
}

func TestInstallActivateUpdateUninstall(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res, err := h.inst.Install(ctx, "ocean")
	if err != nil || res.Version != "1.0.0" {
		t.Fatalf("install: %+v %v", res, err)
	}
	if b, err := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); err != nil || string(b) != "ocean" {
		t.Errorf("template not written: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean", "README.md")); err == nil {
		t.Error("only templates/ and static/ are installed")
	}
	var m registry.ThemeManifest
	if b, err := os.ReadFile(filepath.Join(h.root, "ocean", "goblog-theme.json")); err != nil || json.Unmarshal(b, &m) != nil || m.Name != "ocean" {
		t.Errorf("manifest not written: %v", err)
	}
	if _, err := h.inst.Install(ctx, "ocean"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("install twice: %v", err)
	}
	st := h.inst.Status()
	var ocean Installed
	for _, i := range st.Installed {
		if i.Name == "ocean" {
			ocean = i
		}
	}
	if ocean.Version != "1.0.0" || ocean.Builtin || ocean.UpdateAvailable {
		t.Errorf("ocean status = %+v", ocean)
	}

	if err := h.inst.ActivateTheme("ocean"); err != nil || h.active != "ocean" {
		t.Errorf("activate: %v active=%s", err, h.active)
	}
	if err := h.inst.ActivateTheme("nope"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("activate unknown: %v", err)
	}
	if err := h.inst.Uninstall("ocean"); !errors.Is(err, ErrActive) {
		t.Errorf("uninstall active: %v", err)
	}

	// A newer release appears in the index.
	h.index[0].Version = "1.1.0"
	h.index[0].DownloadURL = h.srv.URL + "/o/ocean/archive/refs/tags/v1.1.0.zip"
	h.index[0].SHA256 = registry.ContentHash(map[string][]byte{"templates/home.html": []byte("ocean 2")})
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	st = h.inst.Status()
	for _, i := range st.Installed {
		if i.Name == "ocean" && (!i.UpdateAvailable || i.LatestVersion != "1.1.0") {
			t.Errorf("update not detected: %+v", i)
		}
	}
	if _, err := h.inst.Update(ctx, "ocean"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "ocean 2" {
		t.Errorf("update not applied: %q", b)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean", "static")); err == nil {
		t.Error("update replaces the whole theme, stale files must not linger")
	}
	if h.activations[len(h.activations)-1] != "ocean" {
		t.Error("updating the active theme must reload it")
	}
	if _, err := h.inst.Update(ctx, "ocean"); !errors.Is(err, ErrUpToDate) {
		t.Errorf("update at latest: %v", err)
	}

	h.active = "default"
	if err := h.inst.Uninstall("ocean"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean")); err == nil {
		t.Error("uninstall must remove the directory")
	}
	if err := h.inst.Uninstall("forest"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("uninstall built-in: %v", err)
	}
	if err := h.inst.Uninstall("ocean"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("uninstall missing: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.inst.Install(ctx, "future"); !errors.Is(err, ErrIncompatible) {
		t.Errorf("incompatible: %v", err)
	}
	if _, err := h.inst.Install(ctx, "forest"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("built-in: %v", err)
	}
	if _, err := h.inst.Install(ctx, "hello"); !errors.Is(err, ErrNotTheme) {
		t.Errorf("plugin entry: %v", err)
	}
	if _, err := h.inst.Install(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := h.inst.Install(ctx, "broken"); !errors.Is(err, ErrLoad) {
		t.Errorf("broken templates: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "broken")); err == nil {
		t.Error("a failed install must leave nothing behind")
	}
	h.index[0].SHA256 = "00"
	h.inst.Refresh()
	if _, err := h.inst.Install(ctx, "ocean"); !errors.Is(err, ErrChecksum) {
		t.Errorf("hash mismatch: %v", err)
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 0 {
		t.Errorf("nothing may be written on a refused install, found %v", entries)
	}
}

func TestUpdateRollsBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.inst.Install(ctx, "ocean"); err != nil {
		t.Fatal(err)
	}
	h.index[0].Version = "1.1.0"
	h.index[0].DownloadURL = h.srv.URL + "/o/ocean/archive/refs/tags/v1.1.0.zip"
	h.index[0].SHA256 = "00" // wrong hash: the download is refused
	h.inst.Refresh()
	if _, err := h.inst.Update(ctx, "ocean"); !errors.Is(err, ErrChecksum) {
		t.Errorf("update: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "ocean" {
		t.Errorf("previous theme must survive a failed update: %q", b)
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 1 {
		t.Errorf("no temp/prev dirs may linger: %v", entries)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./theme/installer/`
Expected: FAIL — package missing.

- [ ] **Step 3: Write `theme/installer/installer.go`**

```go
// Package installer installs, updates and removes themes from the theme
// directory at runtime, for the admin UI. A theme is a directory of
// templates and static files: install means download the release archive,
// check its content hash against the index, check the templates parse, and
// unpack it under the installed root.
package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	pinstaller "goblog/plugin/installer"
	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"
	"goblog/theme"
)

// DefaultIndexURL is the public theme directory index.
const DefaultIndexURL = "https://www.goblog.live/themes/index.json"

// maxArchiveBytes caps a theme download (the registry applies the same).
const maxArchiveBytes = registry.MaxAssetBytes

// staleAfter mirrors the plugin installer: a cached index older than this
// is refreshed on the next Status.
var staleAfter = time.Hour

// Typed errors; the admin API maps them to 4xx responses with their text.
var (
	ErrNotFound             = errors.New("this theme is not in the directory")
	ErrNotTheme             = errors.New("this directory entry is not a theme")
	ErrIncompatible         = errors.New("this theme requires a newer goblog")
	ErrChecksum             = errors.New("the downloaded archive does not match the directory index; the index may be stale, refresh and try again")
	ErrAlreadyInstalled     = errors.New("a theme with this name is already installed")
	ErrNotInstalled         = errors.New("this theme is not installed")
	ErrBuiltin              = errors.New("built-in themes cannot be installed, updated or removed")
	ErrActive               = errors.New("the active theme cannot be removed; activate another theme first")
	ErrLoad                 = errors.New("the theme failed to load")
	ErrDirectoryUnavailable = errors.New("the theme directory index is unavailable")
	ErrUpToDate             = errors.New("theme is already at the directory's version")
	ErrDownload             = errors.New("could not download the theme")
	ErrWrite                = errors.New("could not write to the installed themes directory; check that it exists and goblog can write to it")
)

// Installer wires the themes index, the theme roots and the active-theme
// setting together.
type Installer struct {
	Dir         string              // where installs go (theme.InstalledRoot())
	Directory   *pinstaller.Fetcher // own instance, pointed at the themes index
	Version     string              // running goblog version
	Client      *http.Client        // downloads; nil → 30s timeout default
	IndexURL    func() string       // theme_directory_url setting
	ActiveTheme func() string       // the theme currently rendering
	Activate    func(name string) error // persist the setting and hot-reload

	mu      sync.Mutex // serializes Install/Update/Uninstall/ActivateTheme
	urlMu   sync.Mutex
	lastURL string
}

// Installed is a theme goblog can activate.
type Installed struct {
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version,omitempty"`
	Builtin         bool   `json:"builtin"`
	Active          bool   `json:"active"`
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version,omitempty"`
}

// Available is a directory theme that is not installed.
type Available struct {
	directory.Entry
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// Status is everything the admin page needs in one call.
type Status struct {
	Installed      []Installed `json:"installed"`
	Available      []Available `json:"available"`
	Active         string      `json:"active"`
	DirectoryURL   string      `json:"directory_url"`
	IndexFetchedAt string      `json:"index_fetched_at,omitempty"`
	IndexError     string      `json:"index_error,omitempty"`
	DirWritable    bool        `json:"dir_writable"`
	DirError       string      `json:"dir_error,omitempty"`
}

// Result reports a successful install or update.
type Result struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Message string `json:"message"`
}

// manifestFile is written next to an installed theme so Status can report
// its version without the index.
const manifestFile = "goblog-theme.json"

func allowedScheme(u *url.URL) bool {
	host := u.Hostname()
	return u.Scheme == "https" || (u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1"))
}

func (i *Installer) client() *http.Client {
	base := i.Client
	if base == nil {
		base = &http.Client{Timeout: 60 * time.Second}
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !allowedScheme(req.URL) {
			return fmt.Errorf("redirected to %s: download_url must be https", req.URL)
		}
		return nil
	}
	return &c
}

// Refresh re-fetches the index now.
func (i *Installer) Refresh() error {
	if err := i.Directory.Refresh(i.IndexURL()); err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}
	return nil
}

// ensureIndex mirrors the plugin installer: refetch on a URL change or a
// stale cache, otherwise defer to the fetcher's own throttled first fetch.
func (i *Installer) ensureIndex(indexURL string) {
	i.urlMu.Lock()
	changed := indexURL != i.lastURL
	i.lastURL = indexURL
	i.urlMu.Unlock()
	if changed {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Theme installer: fetching directory index at %s: %v", indexURL, err)
		}
		return
	}
	if fetchedAt := i.Directory.FetchedAt(); !fetchedAt.IsZero() && time.Since(fetchedAt) > staleAfter {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Theme installer: refreshing stale directory index at %s: %v", indexURL, err)
		}
		return
	}
	i.Directory.Ensure(indexURL)
}

// installedVersion reads the manifest an install wrote ("" for built-ins
// or hand-copied themes).
func installedVersion(dir string) (registry.ThemeManifest, bool) {
	b, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return registry.ThemeManifest{}, false
	}
	var m registry.ThemeManifest
	if json.Unmarshal(b, &m) != nil {
		return registry.ThemeManifest{}, false
	}
	return m, true
}

// Status lists every theme on disk (with update info for installed ones)
// and the directory themes that are not installed.
func (i *Installer) Status() Status {
	indexURL := i.IndexURL()
	i.ensureIndex(indexURL)
	_, entries, ok := i.Directory.Index()
	st := Status{Active: i.ActiveTheme(), DirectoryURL: indexURL, Installed: []Installed{}, Available: []Available{}}
	if !ok {
		st.IndexError = "could not fetch the theme directory index"
	} else {
		st.IndexFetchedAt = i.Directory.FetchedAt().UTC().Format(time.RFC3339)
	}
	st.DirWritable, st.DirError = i.probeDirWritable()

	byName := map[string]directory.Entry{}
	for _, e := range entries {
		if e.Kind != registry.KindTheme || !directory.ValidName(e.Name) {
			continue
		}
		e.SourceURL = safeURL(e.SourceURL)
		e.DownloadURL = safeURL(e.DownloadURL)
		e.ScreenshotURL = safeURL(e.ScreenshotURL)
		byName[e.Name] = e
	}
	onDisk := map[string]bool{}
	for _, name := range theme.List() {
		onDisk[name] = true
		row := Installed{Name: name, DisplayName: name, Builtin: theme.IsBuiltin(name), Active: name == st.Active}
		if dir, ok := theme.Dir(name); ok {
			if m, ok := installedVersion(dir); ok {
				row.DisplayName, row.Version = m.DisplayName, m.Version
			}
		}
		if e, ok := byName[name]; ok && !row.Builtin {
			row.LatestVersion = e.Version
			row.UpdateAvailable = row.Version != "" && newer(e.Version, row.Version)
		}
		st.Installed = append(st.Installed, row)
	}
	for _, e := range byName {
		if onDisk[e.Name] {
			continue
		}
		a := Available{Entry: e, Compatible: true}
		if !compatible(i.Version, e.MinGoblogVersion) {
			a.Compatible, a.Reason = false, "requires goblog "+e.MinGoblogVersion+" or newer"
		}
		st.Available = append(st.Available, a)
	}
	sort.SliceStable(st.Available, func(x, y int) bool {
		if st.Available[x].Stars != st.Available[y].Stars {
			return st.Available[x].Stars > st.Available[y].Stars
		}
		return st.Available[x].Name < st.Available[y].Name
	})
	return st
}

// lookup finds a theme entry and applies the checks that need no download.
func (i *Installer) lookup(name string) (directory.Entry, error) {
	if !directory.ValidName(name) || !theme.ValidName(name) {
		return directory.Entry{}, ErrNotFound
	}
	i.ensureIndex(i.IndexURL())
	if _, _, ok := i.Directory.Index(); !ok {
		return directory.Entry{}, ErrDirectoryUnavailable
	}
	e, ok := i.Directory.Entry(name)
	if !ok {
		return directory.Entry{}, ErrNotFound
	}
	if e.Kind != registry.KindTheme || e.InstallType != "theme" {
		return directory.Entry{}, ErrNotTheme
	}
	if !directory.ValidName(e.Name) || !theme.ValidName(e.Name) {
		return directory.Entry{}, fmt.Errorf("%w: invalid theme name %q", ErrLoad, e.Name)
	}
	if !compatible(i.Version, e.MinGoblogVersion) {
		return directory.Entry{}, fmt.Errorf("%w (%s; this is %s)", ErrIncompatible, e.MinGoblogVersion, i.Version)
	}
	return e, nil
}

// fetchAndCheck downloads the archive, verifies its content hash against
// the index and parses its templates. Nothing touches the disk here.
func (i *Installer) fetchAndCheck(ctx context.Context, e directory.Entry) (map[string][]byte, error) {
	zb, err := i.download(ctx, e.DownloadURL)
	if err != nil {
		return nil, err
	}
	files, err := registry.ParseArchive(zb)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if registry.ContentHash(files) != e.SHA256 {
		return nil, ErrChecksum
	}
	if err := theme.ValidateFiles(files); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	return files, nil
}

// Install downloads, verifies and unpacks a directory theme.
func (i *Installer) Install(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return Result{}, ErrBuiltin
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if _, ok := theme.Dir(e.Name); ok {
		return Result{}, ErrAlreadyInstalled
	}
	files, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	if err := i.place(e, files); err != nil {
		return Result{}, err
	}
	log.Printf("Installed theme %s v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Installed %s v%s", e.DisplayName, e.Version)}, nil
}

// Update replaces an installed theme with the index version. The previous
// directory is kept aside until the new one is in place and restored if
// anything fails; the active theme is reloaded afterwards.
func (i *Installer) Update(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return Result{}, ErrBuiltin
	}
	dir, ok := theme.Dir(name)
	if !ok {
		return Result{}, ErrNotInstalled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	current, _ := installedVersion(dir)
	if current.Version != "" && !newer(e.Version, current.Version) {
		return Result{}, fmt.Errorf("%w: %s is at v%s; the directory has v%s", ErrUpToDate, name, current.Version, e.Version)
	}
	files, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	if err := i.place(e, files); err != nil {
		return Result{}, err
	}
	if i.ActiveTheme() == e.Name && i.Activate != nil {
		if err := i.Activate(e.Name); err != nil {
			log.Printf("Theme installer: reloading %s after update: %v", e.Name, err)
		}
	}
	log.Printf("Updated theme %s to v%s", e.Name, e.Version)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Updated %s to v%s", e.DisplayName, e.Version)}, nil
}

// place writes files into a temp dir under Dir and swaps it into
// Dir/<name>, setting an existing directory aside as <name>.prev until the
// swap succeeds. Any failure restores the previous directory.
func (i *Installer) place(e directory.Entry, files map[string][]byte) error {
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	tmp, err := os.MkdirTemp(i.Dir, "."+e.Name+".tmp-")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	defer os.RemoveAll(tmp)
	for p, b := range files {
		full := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
	}
	m := registry.ThemeManifest{Name: e.Name, DisplayName: e.DisplayName, Description: e.Description, Author: e.Author, License: e.License, MinGoblogVersion: e.MinGoblogVersion, Homepage: e.SourceURL}
	m.Homepage = "" // not in the index; leave unset rather than guess
	mb, _ := json.MarshalIndent(struct {
		registry.ThemeManifest
		Version string `json:"version"`
	}{m, e.Version}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, manifestFile), mb, 0o644); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}

	final := filepath.Join(i.Dir, e.Name)
	prev := final + ".prev"
	os.RemoveAll(prev)
	hadPrev := false
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, prev); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
		hadPrev = true
	}
	if err := os.Rename(tmp, final); err != nil {
		if hadPrev {
			os.Rename(prev, final)
		}
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if hadPrev {
		os.RemoveAll(prev)
	}
	return nil
}

// Uninstall removes an installed theme; built-ins and the active theme
// are refused.
func (i *Installer) Uninstall(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return ErrBuiltin
	}
	dir, ok := theme.Dir(name)
	if !ok {
		return ErrNotInstalled
	}
	if i.ActiveTheme() == name {
		return ErrActive
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	log.Printf("Uninstalled theme %s", name)
	return nil
}

// ActivateTheme makes name the site's theme (built-in or installed).
func (i *Installer) ActivateTheme(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := theme.Dir(name); !ok && name != theme.DefaultName {
		return ErrNotInstalled
	}
	if i.Activate == nil {
		return errors.New("theme activation is not wired")
	}
	return i.Activate(name)
}

func (i *Installer) download(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: bad download_url: %v", ErrLoad, err)
	}
	if !allowedScheme(u) {
		return nil, fmt.Errorf("%w: download_url must be https", ErrLoad)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "goblog-theme-installer/"+i.Version)
	resp, err := i.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned HTTP %d", ErrDownload, rawURL, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxArchiveBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if len(b) > maxArchiveBytes {
		return nil, fmt.Errorf("%w: archive is larger than %d bytes", ErrLoad, maxArchiveBytes)
	}
	return b, nil
}

func (i *Installer) probeDirWritable() (bool, string) {
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return false, err.Error()
	}
	f, err := os.CreateTemp(i.Dir, ".probe-*")
	if err != nil {
		return false, err.Error()
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return false, closeErr.Error()
	}
	if removeErr != nil {
		return false, removeErr.Error()
	}
	return true, ""
}

func safeURL(u string) string {
	trimmed := strings.TrimSpace(u)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return trimmed
	}
	return ""
}
```

Version helpers: copy `parseVersion`, `cmp`, `compatible`, `newer` from `plugin/installer/version.go` into `theme/installer/version.go` **or**, better, export them from `plugin/installer` (`installer.Compatible`, `installer.Newer`) and use those — do the export (rename, update the three call sites in `plugin/installer/installer.go`, keep `version_test.go` passing) so there is one implementation.

- [ ] **Step 4: Run the tests**

Run: `gofmt -l theme; go vet ./theme/...; go test ./theme/... ./plugin/installer/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: all ok. If `TestInstallActivateUpdateUninstall` fails on "stale files must not linger", `place` did not replace the whole directory — it must rename, never merge.

- [ ] **Step 5: Commit**

```bash
git add theme plugin/installer && git commit -m "theme/installer: install, update, activate and remove directory themes

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Admin → Themes page, API, wiring, setting; open PR 3

**Files:**
- Create: `admin/themes.go`, `admin/themes_test.go`, `themes/default/templates/admin_themes.html`
- Modify: `admin/admin.go` (`Themes *tinstaller.Installer` field), `themes/default/templates/admin_nav.html`, `goblog.go`, `tools/migrate.go`, `csrf_test.go`, `README.md`

**Interfaces:**
- Consumes: Task 9's `Installer`.
- Produces: routes `GET /admin/themes`, `GET /api/v1/themes/status`, `GET /api/v1/themes/directory?q=&sort=`, `POST /api/v1/themes/install {name}`, `POST /api/v1/themes/update {name}`, `POST /api/v1/themes/activate {name}`, `DELETE /api/v1/themes/:name`, `POST /api/v1/themes/refresh`; site setting `theme_directory_url`.

- [ ] **Step 1: Tests (`admin/themes_test.go`)**

Model on `admin/plugins_test.go`: a harness serving a themes index (one valid theme `ocean` with a zip, per Task 9's fixture — copy `zipOf`), theme roots pointed at temp dirs (as in Task 9's harness), `ad.Themes = &tinstaller.Installer{...}` with `Activate` recording the name. Tests: `TestThemeAPI_NonAdmin` (401 on all seven), `TestThemeAPI_NoInstaller` (503), `TestThemeAPI_Lifecycle` (status shows `default` active + `ocean` available → install 200 → status shows `ocean` installed → activate 200 and the recorder saw "ocean" → delete 409 (`ErrActive`) → activate default → delete 200 → `directory?q=oce` returns one entry → `refresh` 200 → `install` of unknown 404, of incompatible 422/400 per the mapping below).

- [ ] **Step 2: Handlers (`admin/themes.go`)**

Mirror `admin/plugins.go` exactly: `requireThemes(c) *tinstaller.Installer` (401 / 503 "theme installer is not available"); `themeStatus(err) int` mapping `ErrNotFound`/`ErrNotInstalled` → 404, `ErrAlreadyInstalled`/`ErrActive`/`ErrUpToDate` → 409, `ErrBuiltin`/`ErrIncompatible`/`ErrNotTheme`/`ErrChecksum`/`ErrLoad` → 422, `ErrDirectoryUnavailable` → 503, `ErrDownload` → 502, `ErrWrite` → 500 with the message; `writeThemeError`; handlers `ThemeStatus`, `ThemeDirectory` (filter `q` over name/display_name/description/author, `sort` stars|name|newest as in `PluginDirectory`), `InstallTheme`, `UpdateTheme`, `ActivateTheme`, `UninstallTheme`, `RefreshThemeDirectory`, and `AdminThemes` rendering `admin_themes.html` with the same `gin.H` keys as `AdminPlugins`.

- [ ] **Step 3: Template `admin_themes.html`**

Same skeleton as `admin_plugins.html` (header, `admin_nav.html`, `<h1>Themes</h1>`, error/disabled/index-error alerts, two tabs). **Installed** tab: table `Theme · Version · Type · Active · actions`: the Active column shows a ✓ for the active theme and an `Activate` button (`data-action="activate"`) otherwise; actions: `Update to vX` when `update_available`, `Uninstall` for non-built-in, non-active rows. **Browse** tab: search + sort + ↻ like plugins; cards with `<img class="card-img-top">` from `httpURL(t.screenshot_url)`, display name, version, description, author/★/license/`requires goblog ≥`, Source and Directory-page links (`https://www.goblog.live/themes/<safeName>`), `Install` button disabled with reason when incompatible or `dir_writable` is false. JS: copy `esc`, `api`, `httpURL`, `safeName` from `admin_plugins.html`; `loadStatus` → `/api/v1/themes/status`; `pluginAction` becomes `themeAction` posting to `/api/v1/themes/<action>` (`uninstall` → `DELETE /api/v1/themes/<name>`); `activate` confirms nothing (it is reversible), `uninstall` confirms. Add `<a class="nav-link" href="/admin/themes">Themes</a>` after Plugins in `admin_nav.html`. Add `{{ else if eq .Type "password" }}` is already there; nothing else in settings changes — the theme dropdown keeps working because `ListThemes` now includes installed themes.

- [ ] **Step 4: Wiring**

`admin/admin.go`: `Themes *tinstaller.Installer // theme directory install/activate; nil when not wired` (import `tinstaller "goblog/theme/installer"`).

`goblog.go`, after the plugin installer block:

```go
	themeInstaller := &tinstaller.Installer{
		Dir:         theme.InstalledRoot(),
		Directory:   installer.NewFetcher(nil),
		Version:     Version,
		IndexURL:    func() string { return _blog.SettingValue("theme_directory_url", tinstaller.DefaultIndexURL) },
		ActiveTheme: func() string { return activeTheme },
		Activate: func(name string) error {
			if db != nil {
				if err := db.Save(&blog.Setting{Key: "theme", Type: "text", Value: name}).Error; err != nil {
					return err
				}
			}
			loadTheme(name)
			return nil
		},
	}
	themeInstaller.Directory.SetUserAgent("goblog-theme-installer/" + Version)
	_admin.Themes = themeInstaller
```
(place it after `loadTheme` is defined — move the block below the theme-loading code if needed). Routes next to the plugin ones:

```go
	router.GET("/api/v1/themes/status", goblog._admin.ThemeStatus)
	router.GET("/api/v1/themes/directory", goblog._admin.ThemeDirectory)
	router.POST("/api/v1/themes/install", goblog._admin.InstallTheme)
	router.POST("/api/v1/themes/update", goblog._admin.UpdateTheme)
	router.POST("/api/v1/themes/activate", goblog._admin.ActivateTheme)
	router.DELETE("/api/v1/themes/:name", goblog._admin.UninstallTheme)
	router.POST("/api/v1/themes/refresh", goblog._admin.RefreshThemeDirectory)
```
and `g.router.GET("/admin/themes", g._admin.AdminThemes)` next to `/admin/plugins`.

`tools/migrate.go`: add `{Key: "theme_directory_url", Type: "text", Value: "https://www.goblog.live/themes/index.json"},`.

`csrf_test.go`: register `r.POST("/api/v1/themes/activate", ok)` and add rows `{"cross-site theme activate rejected", "POST", "/api/v1/themes/activate", "application/x-www-form-urlencoded", "name=x", 415}` and `{"theme activate allowed", "POST", "/api/v1/themes/activate", "application/json", `{"name":"x"}`, 200}`.

README: under Theming add "**Admin → Themes** browses the [theme directory](https://www.goblog.live/themes), installs a theme into `themes/installed/` (bind-mount it in Docker or installs vanish on restart), activates it, updates it when the directory has a newer release, and removes it. The directory URL is the `theme_directory_url` setting."

- [ ] **Step 5: Verify, run, commit, PR**

Run: `gofmt -l admin theme; go vet ./admin/ ./theme/...; go test ./...` → green. Start the app with a scratch sqlite `.env`, log in is not scriptable — instead exercise the API with the admin test harness only, and check `/admin/themes` returns 200 for an admin session if you have one (otherwise say so in the report).

```bash
git add admin themes/default/templates goblog.go tools/migrate.go csrf_test.go README.md && git commit -m "Admin → Themes: install, update, activate and remove directory themes

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/theme-installer
gh pr create --base main --title "Admin → Themes: one-click theme install from the directory" --body "$(cat <<'BODY'
Part 3 of #560 (spec `docs/superpowers/specs/2026-09-20-theme-directory-design.md`). Stacked on #<PR2 number>.

- `theme/installer`: download the tag archive from `theme_directory_url`, verify the content hash, parse the templates, unpack into `themes/installed/<name>`; update with rollback; uninstall (never the active or a built-in theme); activate (persists the `theme` setting and hot-reloads).
- Admin → Themes page (Installed / Browse with screenshots) and `/api/v1/themes/*`; nav entry; `theme_directory_url` setting (default `https://www.goblog.live/themes/index.json`).
- `csrf_test.go` pins `/api/v1/themes/activate`.

Docker: bind-mount `themes/installed` (iac PR follows) or installs vanish on restart.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

## Other repositories

### Task 11: `goblogplatform/goblog-theme-forest` — the first listed theme

**Files (new repo, worked in a scratch dir):** `goblog-theme.json`, `templates/` (only the files that differ from `themes/default/templates` in goblog at the PR-1 merge commit), `static/` (all of `themes/forest/static`), `README.md`, `screenshot.png`, `LICENSE` (same as goblog's), `CHANGELOG.md` (optional).

- [ ] **Step 1: Assemble** — `gh repo create goblogplatform/goblog-theme-forest --public --description "Forest theme for goblog"`; clone into the scratch dir; for each file in `themes/forest/templates`, keep it only if `diff -q` against `themes/default/templates/<same>` reports a difference (forest was a full copy; the override loader makes the identical ones dead weight); copy `static/` whole.
- [ ] **Step 2: Manifest** — `{"name":"forest","display_name":"Forest","description":"Misty greens and a soft serif — goblog's forest theme.","author":"Jason Ernst","license":"<goblog's SPDX id>","min_goblog_version":"0.5.0"}`. **Stop:** `forest` is in `reservedThemeNames` because it ships compiled in. Ruling for the executor: name it `forest-theme`? No — the reserved list exists so a directory theme can't shadow a built-in; for the built-in forest to be *also* directory-installable, remove `forest` from `reservedThemeNames` in the registry (and from the contract doc) in PR 2 **and** have the installer's `IsBuiltin` check refuse the install with `ErrBuiltin` (already the case). Apply that change in PR 2 before it merges; the theme repo then uses `"name":"forest"`. Users with the built-in forest see it under Installed (built-in) and never under Browse; a future goblog can drop the built-in copy.
- [ ] **Step 3: Screenshot** — run goblog locally with `theme=forest`, open the home page at 1280×800 with a few sample posts, save `screenshot.png` (≤ 1 MiB; use `pngquant`/`optipng` if needed). If no browser is available to the executor, report BLOCKED on this step — the controller takes the screenshot.
- [ ] **Step 4: Validate locally** — from the goblog checkout: `go run ./... ` has no theme validator CLI; instead run `go test ./theme/ -run TestValidateFiles` against the repo by temporarily writing a test — simpler: zip the repo (`git archive --format=zip --prefix=forest-1.0.0/ HEAD > /tmp/forest.zip`) and run a throwaway Go program in the scratch dir that calls `registry.ParseArchive` + `theme.ValidateFiles` with `theme.BuiltinRoot` pointed at the goblog checkout's `themes/` and `theme.SharedDir` at its `templates/shared`. Expect no error.
- [ ] **Step 5: Release** — commit, push, `gh release create v1.0.0 --title v1.0.0 --notes "First release: the forest theme as a directory theme."`. Then on goblog.live (after PR 2 is deployed): Admin → Plugins → Directory → Add repository, kind Theme, `goblogplatform/goblog-theme-forest`.

### Task 12: `goblog-site-theme` — `admin_themes.html` + nav

In a scratch worktree of `~/dev/goblog-site-theme` from `origin/main`: copy `themes/default/templates/admin_themes.html` from PR 3, then re-apply the theme's known divergences to it (the stricter IPv6 regex in `hostNote` is not used by the themes page; the `runtimeBadge` health badge is plugin-only — so a verbatim copy is expected to be correct; diff against the theme's `admin_plugins.html` header/footer includes to confirm the same `{{ template }}` names). Add the Themes nav link to `templates/admin_nav.html`. Commit, push `feat/admin-themes`, PR "Admin → Themes page" referencing PR 3.

### Task 13: iac — persisted `themes/installed`

In `~/dev/iac`: in `ansible/roles/projects/tasks/main.yml` (goblog.live) and `ansible/roles/jasonernst_com/tasks/main.yml` (prod + staging), add a directory task (`/opt/goblog-live/themes-installed`, `/opt/goblog/prod/themes-installed`, `/opt/goblog/staging/themes-installed`, mode 0755, owner root) mirroring the existing `plugins-wasm` ones, and a bind mount `<host dir>:/go/src/github.com/compscidr/goblog/themes/installed` in each `docker_container` volumes list next to the `plugins/wasm` mount. Branch `feat/themes-installed-mount`, PR. Renovate's `v0.5.0` bump can be merged into the same PR or separately.

---

## Self-review

- **Spec coverage:** §1 contract → Tasks 4, 8 (doc), 11 (forest follows it); §2 registry → Tasks 4–5; §3 directory kind/pages/admin tab → Tasks 6–8; §4 loader → Tasks 1–3; §5 installer + Admin → Themes → Tasks 9–10; §6 rollout → Tasks 11–13 (+ release/deploy by the user); §7 tests → each task. Deviation: screenshot cap 1 MiB (Task 8 patches the spec); `forest` un-reserved so the built-in can also be listed (Task 11 Step 2 — apply in PR 2).
- **Placeholders:** none; the two "mirror `admin/plugins.go`" instructions in Task 10 point at concrete existing code and list every handler, mapping and template element.
- **Type consistency:** `theme.Dir/List/IsBuiltin/ValidName/ValidateFiles/InstalledRoot/BuiltinRoot/SharedDir/DefaultName` (T1–2) used by T9/T10; `registry.KindPlugin/KindTheme/ParseArchive/ContentHash/ThemeManifest/FakeThemeValidator/BuildTheme/MaxScreenshotBytes` (T4–5) used by T6/T9; `directory.KindPlugin/KindTheme/ErrBadKind`, `Service.Submit(ctx, kind, …)`/`Add`/`Index(kind)`/`Detail(kind, name)` (T6) used by T7/T8; `Entry.Kind/ScreenshotURL` (T5) used by T9's harness and the admin JS; `pinstaller.Fetcher` methods exist since the plugin-directory work; `installer.Compatible/Newer` exported in T9 and used there.
