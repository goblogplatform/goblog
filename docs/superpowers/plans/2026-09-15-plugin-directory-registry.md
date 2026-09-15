# Plugin Directory Registry (piece B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the seed plugin repo `goblogplatform/goblog-plugin-hello` and the curated registry repo `goblogplatform/plugins`, whose CI validates submissions and publishes `index.json` + per-plugin detail JSON to GitHub Pages for the `directory` plugin (already in goblog v0.2.7) to consume.

**Architecture:** The registry is a small Go module. `internal/registry` holds the logic behind two interfaces — `Source` (GitHub: releases, files at a ref, markdown rendering; implemented with go-github) and `Validator` (runs `goblog validate-plugin` inside the pinned `compscidr/goblog` Docker image) — so everything is unit-tested with `httptest` and a fake validator. `cmd/registry` wires flags/env to `Validate`/`Build`. Two workflows: `validate.yml` on PRs, `publish.yml` on main + 6-hourly cron → `actions/deploy-pages`.

**Tech Stack:** Go 1.26 (`go-github/v92` needs it), `gopkg.in/yaml.v3`, `golang.org/x/mod/semver`, GitHub Actions, GitHub Pages, Docker (in CI only), `gh` CLI for repo/Pages setup.

**Spec:** `docs/superpowers/specs/2026-09-14-plugin-directory-design.md` (in goblog) — sections "Plugin repo contract", "Index format", "Piece B — registry and seed plugin", and "Piece B — concrete layout".

## Global Constraints

- Both repos are **public** under the `goblogplatform` org, licence **Apache-2.0** (copy goblog's `LICENSE`).
- Validation image: `compscidr/goblog:v0.2.7`; entrypoint override `--entrypoint /go/src/github.com/compscidr/goblog/goblog`; always `--network none`.
- Manifest `goblog-plugin.json` fields: `name` (`^[a-z0-9-]+$`), `display_name`, `description`, `author`, `license` (SPDX id from the embedded list), `entry` (default `plugin.go`, must end in `.go`, no `/`), `min_goblog_version` (semver without `v`), optional `homepage`.
- Releases: tag `vX.Y.Z` exactly (`^v\d+\.\d+\.\d+$`); drafts and pre-releases ignored; the tag minus `v` must equal the loaded plugin's `Version()`; the loaded `Name()` must equal the manifest `name`.
- `index.json` entry fields (exact names): `name, display_name, description, version, author, license, source_url, download_url, sha256, min_goblog_version, install_type ("dynamic"), released_at (RFC 3339), detail_url`. Sorted by `name`.
- `plugins/<name>.json` = the entry + `readme_html`, `changelog_html` (`""` when no CHANGELOG.md), `releases: [{version, released_at, notes_html, url}]` newest first.
- `download_url` = `https://raw.githubusercontent.com/<owner>/<repo>/<tag>/<entry>`; `source_url` = `https://github.com/<owner>/<repo>`; `detail_url` = `<base-url>/plugins/<name>.json`; default base-url `https://goblogplatform.github.io/plugins`.
- HTML fields are rendered via GitHub `POST /markdown` in `gfm` mode with `context` = `owner/repo`.
- Build skips a failing entry (logged), publishes the rest, and the process exits **2** when anything was skipped (0 clean, 1 fatal).
- Commit messages: imperative subject; Co-Authored-By trailer `Claude Opus 5 (1M context) <noreply@anthropic.com>`. Work directly on `main` in the two new repos is acceptable **only until each repo's first push** (they are empty); after that, branch + PR as usual. Never force-push.
- Local checkouts: `~/dev/goblog-plugin-hello` and `~/dev/goblog-plugins` (repo name is `plugins`).
- Every CLI/CI command that talks to GitHub uses `GITHUB_TOKEN` (env) when set; unauthenticated works for public repos but is rate-limited — the Actions token is always set.

---

## File structure

**`goblogplatform/goblog-plugin-hello`**: `plugin.go`, `goblog-plugin.json`, `README.md`, `CHANGELOG.md`, `LICENSE`, `.gitignore`.

**`goblogplatform/plugins`**:

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | module `github.com/goblogplatform/plugins`, go 1.26 |
| `registry.yaml` | the curated list |
| `internal/registry/manifest.go` (+`_test`) | `Manifest` parse + `Validate()`; `knownLicenses` |
| `internal/registry/registry.go` (+`_test`) | `LoadRegistry(path) ([]string, error)` |
| `internal/registry/source.go` (+`_test`) | `Source` interface, `Release`, `ErrNotFound`, `GitHubSource` (go-github) |
| `internal/registry/validator.go` (+`_test`) | `Validator` interface, `Info`, `DockerValidator`, `FakeValidator` (test helper, in `_test`) |
| `internal/registry/validate.go` (+`_test`) | `ValidateEntry` |
| `internal/registry/build.go` (+`_test`) | `IndexEntry`, `DetailDoc`, `ReleaseDoc`, `Build` |
| `cmd/registry/main.go` | CLI |
| `docs/CONTRACT.md`, `README.md`, `LICENSE`, `renovate.json`, `.gitignore` | docs/config |
| `.github/workflows/validate.yml`, `publish.yml` | CI |

---

### Task 1: Seed plugin repo `goblogplatform/goblog-plugin-hello`

**Files:** all new, in `~/dev/goblog-plugin-hello`.

**Interfaces:**
- Produces: a public repo with release `v1.0.0` whose `goblog-plugin.json` and `plugin.go` satisfy the contract. Tasks 5–7 use `goblogplatform/goblog-plugin-hello` as the first registry entry and expect `name: hello`, version `1.0.0`, `entry: plugin.go`.

- [ ] **Step 1: Create the repo and clone it**

```bash
gh repo create goblogplatform/goblog-plugin-hello --public \
  --description "Example goblog dynamic plugin: adds a configurable greeting to every page" \
  --clone
mv goblog-plugin-hello ~/dev/goblog-plugin-hello 2>/dev/null || true   # only if gh cloned into the cwd
cd ~/dev/goblog-plugin-hello && git branch --show-current
```
If the default branch is not `main`, run `git checkout -b main`.

- [ ] **Step 2: Write the files**

`plugin.go` — start from `~/dev/goblog/plugins/dynamic/hello.go.example` (copy it verbatim, then replace **only** the header comment, everything from the first line through the line before `package main`) with:

```go
// Hello is the example goblog dynamic plugin: it appends a configurable
// greeting to the footer of every page.
//
// Install: copy this file into your goblog's plugins/dynamic/ directory and
// start goblog with ENABLE_DYNAMIC_PLUGINS=true. Change the text under
// Admin -> Settings -> "Hello (example)". See README.md.
//
// Dynamic plugins are ordinary Go source files interpreted at startup by
// Yaegi. They must be `package main` and define `func NewPlugin() plugin.Plugin`.
// The interpreter exposes the Go standard library and the goblog/plugin
// package; third-party packages such as gin or gorm are not available. That
// means a dynamic plugin can implement Name/DisplayName/Version, Settings,
// TemplateHead and TemplateFooter (as below), but not the hooks whose
// signatures name gin or gorm types (TemplateData, ScheduledJobs, OnInit,
// RenderPage) - write a compiled-in plugin for those. Embed plugin.BasePlugin
// for no-op defaults of everything you don't implement.
```
Confirm the file still has `func (p *HelloPlugin) Name() string { return "hello" }` and `Version() string { return "1.0.0" }`.

`goblog-plugin.json`:
```json
{
  "name": "hello",
  "display_name": "Hello",
  "description": "Appends a configurable greeting to the footer of every page. The example goblog dynamic plugin.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "entry": "plugin.go",
  "min_goblog_version": "0.2.6",
  "homepage": "https://github.com/goblogplatform/goblog-plugin-hello"
}
```

`README.md`:
````markdown
# goblog-plugin-hello

The example [goblog](https://github.com/goblogplatform/goblog) dynamic plugin. It appends a configurable greeting to the footer of every page — the smallest thing that proves a plugin is loaded.

## Install

```bash
curl -o plugins/dynamic/hello.go \
  https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go
ENABLE_DYNAMIC_PLUGINS=true ./goblog
```

Every page now ends with a greeting. Requires goblog 0.2.6 or newer. With Docker, bind-mount `plugins/dynamic/` into the image as described in goblog's README.

## Settings

Under **Admin → Settings → Hello (example)**:

| Setting | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Set to `false` to hide the greeting |
| `message` | `Hello from a dynamic plugin` | The text shown at the bottom of every page |

## Use it as a template

Copy this repository, rename the plugin (`Name()` must be unique — it keys the plugin's settings), and follow the [plugin directory contract](https://github.com/goblogplatform/plugins/blob/main/docs/CONTRACT.md) to publish it.

## License

Apache-2.0.
````

`CHANGELOG.md`:
```markdown
# Changelog

## 1.0.0

- Initial release: configurable footer greeting with `enabled` and `message` settings.
```

`LICENSE`: `cp ~/dev/goblog/LICENSE LICENSE`.

`.gitignore`:
```
*.test
```

- [ ] **Step 3: Verify it loads with goblog v0.2.7**

```bash
cd ~/dev/goblog-plugin-hello
docker run --rm --network none -v "$PWD:/p:ro" \
  --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:v0.2.7 \
  validate-plugin /p/plugin.go
```
Expected: `{"name":"hello","display_name":"Hello (example)","version":"1.0.0"}`. If the `v0.2.7` image is not on Docker Hub yet (release workflow still running), fall back to `cd ~/dev/goblog && go run . validate-plugin ~/dev/goblog-plugin-hello/plugin.go` and note it in the report.

Also `python3 -c 'import json;json.load(open("goblog-plugin.json"))'` → no error.

- [ ] **Step 4: Commit, push, release**

```bash
git add -A && git commit -m "Add the hello example plugin, manifest and docs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin main
gh release create v1.0.0 --title "v1.0.0" --notes "Initial release: configurable footer greeting with \`enabled\` and \`message\` settings."
gh release view v1.0.0 --json tagName,isDraft,isPrerelease,publishedAt
```
Expected: `tagName v1.0.0`, `isDraft false`, `isPrerelease false`, a `publishedAt` timestamp.

---

### Task 2: Registry repo scaffold + manifest parsing

**Files (in `~/dev/goblog-plugins`):**
- Create: `go.mod`, `.gitignore`, `LICENSE`, `internal/registry/manifest.go`
- Test: `internal/registry/manifest_test.go`

**Interfaces:**
- Produces: `type Manifest struct{Name, DisplayName, Description, Author, License, Entry, MinGoblogVersion, Homepage string}` (json tags as the contract), `ParseManifest(b []byte) (Manifest, error)` (parses **and** validates; applies the `plugin.go` default for `entry`), `knownLicenses map[string]bool`. Used by Task 5.

- [ ] **Step 1: Create the repo and module**

```bash
gh repo create goblogplatform/plugins --public \
  --description "Curated registry of goblog plugins; builds the index behind goblog.live/plugins" --clone
mv plugins ~/dev/goblog-plugins 2>/dev/null || true
cd ~/dev/goblog-plugins && git branch --show-current
cp ~/dev/goblog/LICENSE LICENSE
printf 'dist/\n*.test\n' > .gitignore
go mod init github.com/goblogplatform/plugins
go mod edit -go=1.26
go get github.com/google/go-github/v92@v92.0.0 gopkg.in/yaml.v3@v3.0.1 golang.org/x/mod@v0.41.0
```

- [ ] **Step 2: Write the failing test**

`internal/registry/manifest_test.go`:

```go
package registry

import (
	"strings"
	"testing"
)

const goodManifest = `{
  "name": "hello",
  "display_name": "Hello",
  "description": "Says hi.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "entry": "plugin.go",
  "min_goblog_version": "0.2.6",
  "homepage": "https://example.test"
}`

func TestParseManifest_Good(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hello" || m.DisplayName != "Hello" || m.License != "Apache-2.0" || m.Entry != "plugin.go" || m.MinGoblogVersion != "0.2.6" || m.Homepage != "https://example.test" {
		t.Errorf("unexpected manifest: %+v", m)
	}
}

func TestParseManifest_EntryDefaultsToPluginGo(t *testing.T) {
	m, err := ParseManifest([]byte(strings.Replace(goodManifest, `"entry": "plugin.go",`, "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Entry != "plugin.go" {
		t.Errorf("entry should default to plugin.go, got %q", m.Entry)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	cases := map[string]string{
		"not json":            `{`,
		"missing name":        strings.Replace(goodManifest, `"name": "hello",`, "", 1),
		"bad name":            strings.Replace(goodManifest, `"name": "hello"`, `"name": "Hello_World"`, 1),
		"missing display":     strings.Replace(goodManifest, `"display_name": "Hello",`, "", 1),
		"missing description": strings.Replace(goodManifest, `"description": "Says hi.",`, "", 1),
		"missing author":      strings.Replace(goodManifest, `"author": "Jason Ernst",`, "", 1),
		"unknown license":     strings.Replace(goodManifest, `"Apache-2.0"`, `"MyLicense"`, 1),
		"entry not go":        strings.Replace(goodManifest, `"plugin.go"`, `"plugin.txt"`, 1),
		"entry with slash":    strings.Replace(goodManifest, `"plugin.go"`, `"src/plugin.go"`, 1),
		"min version with v":  strings.Replace(goodManifest, `"0.2.6"`, `"v0.2.6"`, 1),
		"min version junk":    strings.Replace(goodManifest, `"0.2.6"`, `"latest"`, 1),
		"missing min version": strings.Replace(goodManifest, `"min_goblog_version": "0.2.6",`, "", 1),
	}
	for name, src := range cases {
		if _, err := ParseManifest([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `cd ~/dev/goblog-plugins && go test ./internal/registry/ -run TestParseManifest -v`
Expected: FAIL to compile — `undefined: ParseManifest`.

- [ ] **Step 4: Implement**

`internal/registry/manifest.go`:

```go
// Package registry validates goblog plugin repositories and builds the
// directory index published to GitHub Pages.
package registry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// Manifest is goblog-plugin.json at the root of a plugin repository.
type Manifest struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Author           string `json:"author"`
	License          string `json:"license"`
	Entry            string `json:"entry"`
	MinGoblogVersion string `json:"min_goblog_version"`
	Homepage         string `json:"homepage"`
}

// NamePattern is the rule for plugin names; the directory routes on it.
var NamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// knownLicenses is the set of SPDX identifiers accepted in a manifest. It is
// deliberately short; add to it when a submission needs another one.
var knownLicenses = map[string]bool{
	"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true,
	"ISC": true, "MPL-2.0": true, "Unlicense": true, "0BSD": true,
	"GPL-2.0-only": true, "GPL-2.0-or-later": true, "GPL-3.0-only": true, "GPL-3.0-or-later": true,
	"LGPL-2.1-only": true, "LGPL-2.1-or-later": true, "LGPL-3.0-only": true, "LGPL-3.0-or-later": true,
	"AGPL-3.0-only": true, "AGPL-3.0-or-later": true,
}

// ParseManifest decodes and validates a manifest. Entry defaults to plugin.go.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("goblog-plugin.json: %w", err)
	}
	if m.Entry == "" {
		m.Entry = "plugin.go"
	}
	var problems []string
	if !NamePattern.MatchString(m.Name) {
		problems = append(problems, "name must match ^[a-z0-9-]+$")
	}
	for field, v := range map[string]string{"display_name": m.DisplayName, "description": m.Description, "author": m.Author} {
		if strings.TrimSpace(v) == "" {
			problems = append(problems, field+" is required")
		}
	}
	if !knownLicenses[m.License] {
		problems = append(problems, fmt.Sprintf("license %q is not a known SPDX identifier", m.License))
	}
	if !strings.HasSuffix(m.Entry, ".go") || strings.Contains(m.Entry, "/") {
		problems = append(problems, "entry must be a .go file at the repository root")
	}
	if strings.HasPrefix(m.MinGoblogVersion, "v") || !semver.IsValid("v"+m.MinGoblogVersion) || semver.Prerelease("v"+m.MinGoblogVersion) != "" {
		problems = append(problems, "min_goblog_version must be a plain semver like 0.2.6")
	}
	if len(problems) > 0 {
		return Manifest{}, fmt.Errorf("goblog-plugin.json: %s", strings.Join(problems, "; "))
	}
	return m, nil
}
```

- [ ] **Step 5: Run the tests, commit**

Run: `go test ./internal/registry/ -v`
Expected: PASS (3 tests).

```bash
git add -A && git commit -m "Scaffold the registry module and parse plugin manifests

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin main
```

---

### Task 3: `registry.yaml` loader and the GitHub `Source`

**Files:**
- Create: `registry.yaml`, `internal/registry/registry.go`, `internal/registry/source.go`
- Test: `internal/registry/registry_test.go`, `internal/registry/source_test.go`

**Interfaces:**
- Produces:
  - `LoadRegistry(path string) ([]string, error)` — repos as `owner/name`, in file order; errors on empty list, malformed entries, duplicates.
  - `type Release struct{Tag, Name, Body, URL string; PublishedAt time.Time; Draft, Prerelease bool}`
  - `var ErrNotFound = errors.New("not found")`
  - `type Source interface { Releases(ctx, owner, repo string) ([]Release, error); File(ctx, owner, repo, ref, path string) ([]byte, error); RenderMarkdown(ctx, ownerRepo, markdown string) (string, error) }` — `File` returns `ErrNotFound` (wrapped) on 404.
  - `NewGitHubSource(token, baseURL string) (*GitHubSource, error)` — `baseURL` empty for api.github.com (tests pass an `httptest` URL).
- Tasks 5–7 depend on these.

- [ ] **Step 1: Write the failing tests**

`internal/registry/registry_test.go`:

```go
package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRegistry(t *testing.T) {
	repos, err := LoadRegistry(writeTemp(t, "registry.yaml", "plugins:\n  - repo: goblogplatform/goblog-plugin-hello\n  - repo: someone/goblog-plugin-x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0] != "goblogplatform/goblog-plugin-hello" || repos[1] != "someone/goblog-plugin-x" {
		t.Errorf("repos = %v", repos)
	}
}

func TestLoadRegistry_Errors(t *testing.T) {
	cases := map[string]string{
		"empty":       "plugins: []\n",
		"no key":      "repos:\n  - repo: a/b\n",
		"bad repo":    "plugins:\n  - repo: not-a-repo\n",
		"url repo":    "plugins:\n  - repo: https://github.com/a/b\n",
		"duplicate":   "plugins:\n  - repo: a/b\n  - repo: a/b\n",
		"not yaml":    "plugins: [\n",
	}
	for name, src := range cases {
		if _, err := LoadRegistry(writeTemp(t, "registry.yaml", src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := LoadRegistry(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file: expected an error")
	}
}
```

`internal/registry/source_test.go`:

```go
package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves the three REST endpoints GitHubSource uses.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"tag_name":"v1.1.0","name":"v1.1.0","body":"Second","draft":false,"prerelease":false,"published_at":"2026-09-15T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.1.0"},
		  {"tag_name":"v1.2.0-rc1","name":"rc","body":"","draft":false,"prerelease":true,"published_at":"2026-09-16T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.2.0-rc1"},
		  {"tag_name":"v1.0.0","name":"v1.0.0","body":"First","draft":false,"prerelease":false,"published_at":"2026-09-14T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.0.0"}
		]`))
	})
	mux.HandleFunc("GET /repos/o/r/contents/plugin.go", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "v1.1.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"file","encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte("package main\n")) + `"}`))
	})
	mux.HandleFunc("POST /markdown", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text, Mode, Context string }
		if err := jsonDecode(r, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Mode != "gfm" || body.Context != "o/r" {
			http.Error(w, "expected gfm mode with repo context", 400)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<p>" + body.Text + "</p>"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubSource(t *testing.T) {
	srv := fakeGitHub(t)
	src, err := NewGitHubSource("", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	rels, err := src.Releases(ctx, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 3 || rels[0].Tag != "v1.1.0" || rels[1].Prerelease != true || rels[2].Body != "First" || rels[0].URL == "" || rels[0].PublishedAt.IsZero() {
		t.Errorf("releases = %+v", rels)
	}

	b, err := src.File(ctx, "o", "r", "v1.1.0", "plugin.go")
	if err != nil || string(b) != "package main\n" {
		t.Errorf("File: %q %v", b, err)
	}
	if _, err := src.File(ctx, "o", "r", "v9.9.9", "plugin.go"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing ref should be ErrNotFound, got %v", err)
	}
	if _, err := src.File(ctx, "o", "r", "v1.1.0", "CHANGELOG.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file should be ErrNotFound, got %v", err)
	}

	html, err := src.RenderMarkdown(ctx, "o/r", "hi")
	if err != nil || !strings.Contains(html, "<p>hi</p>") {
		t.Errorf("RenderMarkdown: %q %v", html, err)
	}
	if html, err := src.RenderMarkdown(ctx, "o/r", ""); err != nil || html != "" {
		t.Errorf("empty markdown should render to empty string without a request, got %q %v", html, err)
	}
}
```

Add to the same test file the small helper the fake uses:

```go
func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
```
(and import `encoding/json`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/registry/ -run 'TestLoadRegistry|TestGitHubSource' -v`
Expected: FAIL to compile — `undefined: LoadRegistry`, `NewGitHubSource`, `ErrNotFound`.

- [ ] **Step 3: Implement**

`registry.yaml` (repo root):
```yaml
# Curated list of goblog plugin repositories. To publish a plugin, open a PR
# that adds your repository here — see docs/CONTRACT.md for what the
# repository must contain. CI validates every entry on each PR.
plugins:
  - repo: goblogplatform/goblog-plugin-hello
```

`internal/registry/registry.go`:

```go
package registry

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// LoadRegistry reads registry.yaml and returns its repositories as
// owner/name strings in file order.
func LoadRegistry(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Plugins []struct {
			Repo string `yaml:"repo"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Plugins) == 0 {
		return nil, fmt.Errorf("%s: no plugins listed", path)
	}
	seen := map[string]bool{}
	repos := make([]string, 0, len(doc.Plugins))
	for i, p := range doc.Plugins {
		if !repoPattern.MatchString(p.Repo) {
			return nil, fmt.Errorf("%s: entry %d: repo %q must be owner/name", path, i+1, p.Repo)
		}
		if seen[p.Repo] {
			return nil, fmt.Errorf("%s: repo %q listed twice", path, p.Repo)
		}
		seen[p.Repo] = true
		repos = append(repos, p.Repo)
	}
	return repos, nil
}
```

`internal/registry/source.go`:

```go
package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/go-github/v92/github"
)

// ErrNotFound is returned by Source.File when the ref or path does not exist.
var ErrNotFound = errors.New("not found")

// Release is one GitHub release of a plugin repository.
type Release struct {
	Tag         string
	Name        string
	Body        string // release notes, markdown
	URL         string
	PublishedAt time.Time
	Draft       bool
	Prerelease  bool
}

// Source is what the registry needs from GitHub. It is an interface so the
// validator and builder are tested against an httptest fake.
type Source interface {
	// Releases lists all releases, newest first as GitHub returns them,
	// including drafts and pre-releases (callers filter).
	Releases(ctx context.Context, owner, repo string) ([]Release, error)
	// File returns the contents of path at ref; ErrNotFound when absent.
	File(ctx context.Context, owner, repo, ref, path string) ([]byte, error)
	// RenderMarkdown renders GitHub-flavoured markdown to sanitized HTML in
	// the context of ownerRepo (so #123 and relative links resolve).
	RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error)
}

// GitHubSource implements Source with the GitHub REST API.
type GitHubSource struct {
	client *github.Client
}

// NewGitHubSource returns a Source for api.github.com (baseURL "") or a
// test server. token may be empty for unauthenticated access.
func NewGitHubSource(token, baseURL string) (*GitHubSource, error) {
	opts := []github.ClientOptionsFunc{
		github.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		github.WithUserAgent("goblog-plugin-registry"),
	}
	if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}
	if baseURL != "" {
		opts = append(opts, github.WithURLs(&baseURL, &baseURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &GitHubSource{client: c}, nil
}

func (g *GitHubSource) Releases(ctx context.Context, owner, repo string) ([]Release, error) {
	var out []Release
	opts := &github.ListOptions{PerPage: 100}
	for {
		page, resp, err := g.client.Repositories.ListReleases(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list releases for %s/%s: %w", owner, repo, err)
		}
		for _, r := range page {
			rel := Release{Tag: r.TagName, URL: r.HTMLURL, Draft: r.Draft, Prerelease: r.Prerelease}
			if r.Name != nil {
				rel.Name = *r.Name
			}
			if r.Body != nil {
				rel.Body = *r.Body
			}
			if r.PublishedAt != nil {
				rel.PublishedAt = r.PublishedAt.Time
			}
			out = append(out, rel)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (g *GitHubSource) File(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	fc, _, resp, err := g.client.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s/%s@%s:%s: %w", owner, repo, ref, path, ErrNotFound)
		}
		return nil, fmt.Errorf("get %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	if fc == nil {
		return nil, fmt.Errorf("%s/%s@%s:%s is not a file", owner, repo, ref, path)
	}
	s, err := fc.GetContent()
	if err != nil {
		return nil, fmt.Errorf("decode %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	return []byte(s), nil
}

func (g *GitHubSource) RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error) {
	if markdown == "" {
		return "", nil
	}
	html, _, err := g.client.Markdown.Render(ctx, markdown, &github.MarkdownOptions{Mode: "gfm", Context: ownerRepo})
	if err != nil {
		return "", fmt.Errorf("render markdown for %s: %w", ownerRepo, err)
	}
	return html, nil
}
```

If `go-github` v92 names differ from the above (e.g. `r.TagName` is a `*string`), check with `go doc github.com/google/go-github/v92/github RepositoryRelease` and adapt — the field set in v92 is: `TagName string`, `Name *string`, `Body *string`, `Draft bool`, `Prerelease bool`, `PublishedAt *Timestamp`, `HTMLURL string`.

- [ ] **Step 4: Run tests, tidy, commit**

Run: `go mod tidy && go test ./internal/registry/ -v && go vet ./...`
Expected: PASS for all; vet silent.

```bash
git add -A && git commit -m "Load registry.yaml and read releases, files and rendered markdown from GitHub

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push
```

---

### Task 4: `Validator` (Docker) and `ValidateEntry`

**Files:**
- Create: `internal/registry/validator.go`, `internal/registry/validate.go`
- Test: `internal/registry/validator_test.go`, `internal/registry/validate_test.go`

**Interfaces:**
- Consumes: `Source`, `Release`, `ErrNotFound`, `ParseManifest`, `Manifest`, `NamePattern` (Tasks 2–3).
- Produces:
  - `type Info struct{Name, DisplayName, Version string}` (json tags `name`, `display_name`, `version`)
  - `type Validator interface{ Validate(ctx context.Context, src []byte) (Info, error) }`
  - `NewDockerValidator(image string) *DockerValidator` with `Validate` running `docker run --rm --network none -v <tmp>:/p:ro --entrypoint /go/src/github.com/compscidr/goblog/goblog <image> validate-plugin /p/plugin.go`.
  - `type FakeValidator struct{ Infos map[string]Info; Err error }` (in `validator_test.go`, keyed by sha256 of src) — reused by Task 5 tests.
  - `type Validated struct{ Repo, Owner, Name string; Manifest Manifest; Release Release; Version string; Releases []Release; Entry []byte; SHA256 string }`
  - `ValidateEntry(ctx, src Source, val Validator, repo string) (*Validated, error)`
  - `var TagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)`

- [ ] **Step 1: Write the failing tests**

`internal/registry/validator_test.go`:

```go
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"testing"
)

// FakeValidator answers by the sha256 of the source it is given.
type FakeValidator struct {
	Infos map[string]Info
	Err   error
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (f *FakeValidator) Validate(_ context.Context, src []byte) (Info, error) {
	if f.Err != nil {
		return Info{}, f.Err
	}
	if info, ok := f.Infos[sum(src)]; ok {
		return info, nil
	}
	return Info{}, errors.New("fake: does not load")
}

// TestDockerValidator_Real runs the actual goblog image; skipped unless
// docker is available and REGISTRY_DOCKER_TESTS=1 (it pulls ~100 MB).
func TestDockerValidator_Real(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("REGISTRY_DOCKER_TESTS") == "" {
		t.Skip("set REGISTRY_DOCKER_TESTS=1 with docker available")
	}
	src := []byte(`package main
import "goblog/plugin"
type P struct{ plugin.BasePlugin }
func NewPlugin() plugin.Plugin { return &P{} }
func (P) Name() string { return "p" }
func (P) DisplayName() string { return "P" }
func (P) Version() string { return "1.2.3" }
`)
	v := NewDockerValidator("compscidr/goblog:v0.2.7")
	info, err := v.Validate(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "p" || info.Version != "1.2.3" {
		t.Errorf("info = %+v", info)
	}
	if _, err := v.Validate(context.Background(), []byte("package main\nfunc NewPlugin() int { return 1 ")); err == nil {
		t.Error("broken source should fail")
	}
}

func TestDockerValidator_CommandShape(t *testing.T) {
	v := NewDockerValidator("compscidr/goblog:v0.2.7")
	args := v.args("/tmp/x")
	want := []string{"run", "--rm", "--network", "none", "-v", "/tmp/x:/p:ro",
		"--entrypoint", "/go/src/github.com/compscidr/goblog/goblog", "compscidr/goblog:v0.2.7",
		"validate-plugin", "/p/plugin.go"}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}
```
(add `"os"` to the imports.)

`internal/registry/validate_test.go`:

```go
package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// memSource is an in-memory Source for validator/builder tests.
type memSource struct {
	releases map[string][]Release          // "owner/repo" → releases
	files    map[string]string             // "owner/repo@ref:path" → content
	rendered int                           // RenderMarkdown call count
}

func (m *memSource) Releases(_ context.Context, owner, repo string) ([]Release, error) {
	rels, ok := m.releases[owner+"/"+repo]
	if !ok {
		return nil, errors.New("no such repo")
	}
	return rels, nil
}

func (m *memSource) File(_ context.Context, owner, repo, ref, path string) ([]byte, error) {
	if c, ok := m.files[owner+"/"+repo+"@"+ref+":"+path]; ok {
		return []byte(c), nil
	}
	return nil, ErrNotFound
}

func (m *memSource) RenderMarkdown(_ context.Context, ownerRepo, md string) (string, error) {
	if md == "" {
		return "", nil
	}
	m.rendered++
	return "<p>" + md + "</p>", nil
}

const helloSrc = "package main\n// hello plugin\n"

func helloSource() *memSource {
	return &memSource{
		releases: map[string][]Release{
			"o/hello": {
				{Tag: "v1.1.0", Body: "Second", URL: "https://github.com/o/hello/releases/tag/v1.1.0", PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
				{Tag: "v2.0.0-rc1", Prerelease: true, PublishedAt: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
				{Tag: "v3.0.0", Draft: true, PublishedAt: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
				{Tag: "v1.0.0", Body: "First", URL: "https://github.com/o/hello/releases/tag/v1.0.0", PublishedAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)},
			},
		},
		files: map[string]string{
			"o/hello@v1.1.0:goblog-plugin.json": goodManifest,
			"o/hello@v1.1.0:plugin.go":          helloSrc,
			"o/hello@v1.1.0:README.md":          "# Hello",
			"o/hello@v1.1.0:CHANGELOG.md":       "## 1.1.0\n- second",
		},
	}
}

func helloValidator() *FakeValidator {
	return &FakeValidator{Infos: map[string]Info{sum([]byte(helloSrc)): {Name: "hello", DisplayName: "Hello", Version: "1.1.0"}}}
}

func TestValidateEntry_Good(t *testing.T) {
	v, err := ValidateEntry(context.Background(), helloSource(), helloValidator(), "o/hello")
	if err != nil {
		t.Fatal(err)
	}
	if v.Owner != "o" || v.Name != "hello" || v.Manifest.Name != "hello" || v.Release.Tag != "v1.1.0" || v.Version != "1.1.0" {
		t.Errorf("validated = %+v", v)
	}
	if len(v.Releases) != 2 || v.Releases[0].Tag != "v1.1.0" || v.Releases[1].Tag != "v1.0.0" {
		t.Errorf("releases should exclude drafts/prereleases, newest first: %+v", v.Releases)
	}
	if string(v.Entry) != helloSrc || v.SHA256 != sum([]byte(helloSrc)) {
		t.Errorf("entry/sha mismatch")
	}
}

func TestValidateEntry_PicksLatestByDate(t *testing.T) {
	src := helloSource()
	// GitHub order is not trusted: put the older release first.
	rels := src.releases["o/hello"]
	src.releases["o/hello"] = []Release{rels[3], rels[0]}
	v, err := ValidateEntry(context.Background(), src, helloValidator(), "o/hello")
	if err != nil {
		t.Fatal(err)
	}
	if v.Release.Tag != "v1.1.0" {
		t.Errorf("latest should be v1.1.0 by published date, got %s", v.Release.Tag)
	}
}

func TestValidateEntry_Errors(t *testing.T) {
	type tc struct {
		mutate func(s *memSource, f *FakeValidator)
		want   string
	}
	cases := map[string]tc{
		"bad repo string": {func(s *memSource, f *FakeValidator) {}, "owner/name"},
		"no releases": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"] = []Release{{Tag: "v9.0.0", Draft: true}}
		}, "no published release"},
		"tag not semver": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"][0].Tag = "1.1.0"
			s.files["o/hello@1.1.0:goblog-plugin.json"] = goodManifest
		}, "vX.Y.Z"},
		"missing manifest": {func(s *memSource, f *FakeValidator) {
			delete(s.files, "o/hello@v1.1.0:goblog-plugin.json")
		}, "goblog-plugin.json"},
		"invalid manifest": {func(s *memSource, f *FakeValidator) {
			s.files["o/hello@v1.1.0:goblog-plugin.json"] = `{"name":"Bad"}`
		}, "goblog-plugin.json"},
		"missing entry": {func(s *memSource, f *FakeValidator) {
			delete(s.files, "o/hello@v1.1.0:plugin.go")
		}, "plugin.go"},
		"missing readme": {func(s *memSource, f *FakeValidator) {
			delete(s.files, "o/hello@v1.1.0:README.md")
		}, "README.md"},
		"does not load": {func(s *memSource, f *FakeValidator) {
			f.Err = errors.New("yaegi: boom")
		}, "boom"},
		"name mismatch": {func(s *memSource, f *FakeValidator) {
			f.Infos[sum([]byte(helloSrc))] = Info{Name: "other", Version: "1.1.0"}
		}, "Name()"},
		"version mismatch": {func(s *memSource, f *FakeValidator) {
			f.Infos[sum([]byte(helloSrc))] = Info{Name: "hello", Version: "1.0.9"}
		}, "Version()"},
	}
	for name, c := range cases {
		s, f := helloSource(), helloValidator()
		c.mutate(s, f)
		repo := "o/hello"
		if name == "bad repo string" {
			repo = "hello"
		}
		_, err := ValidateEntry(context.Background(), s, f, repo)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/registry/ -run 'TestDockerValidator|TestValidateEntry' -v`
Expected: FAIL to compile — `undefined: NewDockerValidator`, `ValidateEntry`, `Info`.

- [ ] **Step 3: Implement**

`internal/registry/validator.go`:

```go
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info is what `goblog validate-plugin` prints for a plugin file.
type Info struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
}

// Validator loads a plugin source file the way goblog would and reports its
// identity. The real one runs goblog's validate-plugin in Docker; tests use
// a fake.
type Validator interface {
	Validate(ctx context.Context, src []byte) (Info, error)
}

// GoblogEntrypoint is the goblog binary inside the release image, whose
// ENTRYPOINT is a shell command and therefore has to be overridden.
const GoblogEntrypoint = "/go/src/github.com/compscidr/goblog/goblog"

// DockerValidator runs `goblog validate-plugin` inside the pinned goblog
// image with networking disabled: the file is interpreted, so it can run
// arbitrary Go, and this is the only sandbox the registry gives it.
type DockerValidator struct {
	Image string
}

func NewDockerValidator(image string) *DockerValidator { return &DockerValidator{Image: image} }

func (d *DockerValidator) args(dir string) []string {
	return []string{"run", "--rm", "--network", "none", "-v", dir + ":/p:ro",
		"--entrypoint", GoblogEntrypoint, d.Image, "validate-plugin", "/p/plugin.go"}
}

func (d *DockerValidator) Validate(ctx context.Context, src []byte) (Info, error) {
	dir, err := os.MkdirTemp("", "goblog-plugin-")
	if err != nil {
		return Info{}, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "plugin.go"), src, 0644); err != nil {
		return Info{}, err
	}
	cmd := exec.CommandContext(ctx, "docker", d.args(dir)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Info{}, fmt.Errorf("validate-plugin failed: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	var info Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return Info{}, fmt.Errorf("validate-plugin printed %q: %w", stdout.String(), err)
	}
	return info, nil
}
```

`internal/registry/validate.go`:

```go
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TagPattern is the release tag rule: vX.Y.Z, nothing else.
var TagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Validated is a plugin repository that passed every check, with everything
// the index builder needs from it.
type Validated struct {
	Repo     string // owner/name
	Owner    string
	Name     string // repository name (not the plugin name)
	Manifest Manifest
	Release  Release   // the latest published, non-prerelease release
	Version  string    // Release.Tag without the leading v
	Releases []Release // all published, non-prerelease releases, newest first
	Entry    []byte    // the plugin source at Release.Tag
	SHA256   string    // hex sha256 of Entry
}

// ValidateEntry checks one registry entry end to end: a published release
// tagged vX.Y.Z, a valid manifest and README at that tag, an entry file that
// loads in goblog, and an identity that matches the manifest and the tag.
func ValidateEntry(ctx context.Context, src Source, val Validator, repo string) (*Validated, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%q: repo must be owner/name", repo)
	}

	all, err := src.Releases(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	var releases []Release
	for _, r := range all {
		if !r.Draft && !r.Prerelease {
			releases = append(releases, r)
		}
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("%s: no published release (drafts and pre-releases are ignored)", repo)
	}
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].PublishedAt.After(releases[j].PublishedAt) })
	latest := releases[0]
	if !TagPattern.MatchString(latest.Tag) {
		return nil, fmt.Errorf("%s: release tag %q must be vX.Y.Z", repo, latest.Tag)
	}
	version := strings.TrimPrefix(latest.Tag, "v")

	mb, err := src.File(ctx, owner, name, latest.Tag, "goblog-plugin.json")
	if err != nil {
		return nil, fmt.Errorf("%s@%s: goblog-plugin.json: %w", repo, latest.Tag, err)
	}
	manifest, err := ParseManifest(mb)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %w", repo, latest.Tag, err)
	}
	if _, err := src.File(ctx, owner, name, latest.Tag, "README.md"); err != nil {
		return nil, fmt.Errorf("%s@%s: README.md: %w", repo, latest.Tag, err)
	}
	entry, err := src.File(ctx, owner, name, latest.Tag, manifest.Entry)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: entry %s: %w", repo, latest.Tag, manifest.Entry, err)
	}

	info, err := val.Validate(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %s does not load: %w", repo, latest.Tag, manifest.Entry, err)
	}
	if info.Name != manifest.Name {
		return nil, fmt.Errorf("%s@%s: Name() is %q but the manifest says %q", repo, latest.Tag, info.Name, manifest.Name)
	}
	if info.Version != version {
		return nil, fmt.Errorf("%s@%s: Version() is %q but the release tag says %q", repo, latest.Tag, info.Version, version)
	}

	h := sha256.Sum256(entry)
	return &Validated{
		Repo: repo, Owner: owner, Name: name,
		Manifest: manifest, Release: latest, Version: version, Releases: releases,
		Entry: entry, SHA256: hex.EncodeToString(h[:]),
	}, nil
}

// isNotFound reports whether err is a missing-file error from a Source.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
```

- [ ] **Step 4: Run tests, commit**

Run: `go test ./internal/registry/ -v && go vet ./...`
Expected: PASS (Docker test skipped unless `REGISTRY_DOCKER_TESTS=1`). Then, once, with Docker: `REGISTRY_DOCKER_TESTS=1 go test ./internal/registry/ -run TestDockerValidator_Real -v` → PASS (report if the image isn't published yet).

```bash
git add -A && git commit -m "Validate a plugin repository: release tag, manifest, entry and identity

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push
```

---

### Task 5: `Build` — index.json, detail JSON, index.html

**Files:**
- Create: `internal/registry/build.go`
- Test: `internal/registry/build_test.go`

**Interfaces:**
- Consumes: `ValidateEntry`, `Validated`, `Source.RenderMarkdown`, `Source.File`, `isNotFound`, and the test helpers `memSource`, `helloSource`, `helloValidator`, `FakeValidator`, `sum` from Task 4's tests.
- Produces:
  - `type IndexEntry struct` with json fields `name, display_name, description, version, author, license, source_url, download_url, sha256, min_goblog_version, install_type, released_at, detail_url`.
  - `type ReleaseDoc struct{Version, ReleasedAt, NotesHTML, URL string}` (json `version, released_at, notes_html, url`).
  - `type DetailDoc struct{ IndexEntry; ReadmeHTML, ChangelogHTML string; Releases []ReleaseDoc }` (json `readme_html, changelog_html, releases`; `IndexEntry` inlined via embedding).
  - `type BuildResult struct{ Built []string; Skipped map[string]error }`
  - `Build(ctx, src Source, val Validator, repos []string, outDir, baseURL string) (BuildResult, error)` — writes `<outDir>/index.json`, `<outDir>/plugins/<name>.json`, `<outDir>/index.html`, `<outDir>/.nojekyll`.
  - Task 6 (CLI) uses `Build` and `BuildResult`.

- [ ] **Step 1: Write the failing test**

`internal/registry/build_test.go`:

```go
package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuild_WritesIndexAndDetails(t *testing.T) {
	src := helloSource()
	// A second plugin, older release, to check sorting and skipping.
	src.releases["o/zeta"] = []Release{{Tag: "v0.1.0", Body: "z", URL: "https://github.com/o/zeta/releases/tag/v0.1.0", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	src.files["o/zeta@v0.1.0:goblog-plugin.json"] = strings.Replace(strings.Replace(goodManifest, `"hello"`, `"zeta"`, 1), `"Hello"`, `"Zeta"`, 1)
	src.files["o/zeta@v0.1.0:plugin.go"] = "package main // zeta\n"
	src.files["o/zeta@v0.1.0:README.md"] = "# Zeta"
	val := helloValidator()
	val.Infos[sum([]byte("package main // zeta\n"))] = Info{Name: "zeta", DisplayName: "Zeta", Version: "0.1.0"}

	out := t.TempDir()
	res, err := Build(context.Background(), src, val, []string{"o/zeta", "o/hello"}, out, "https://example.test/plugins")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Built) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("result = %+v", res)
	}

	var index []IndexEntry
	mustJSON(t, filepath.Join(out, "index.json"), &index)
	if len(index) != 2 || index[0].Name != "hello" || index[1].Name != "zeta" {
		t.Fatalf("index should be sorted by name: %+v", index)
	}
	e := index[0]
	want := IndexEntry{
		Name: "hello", DisplayName: "Hello", Description: "Says hi.", Version: "1.1.0", Author: "Jason Ernst",
		License: "Apache-2.0", SourceURL: "https://github.com/o/hello",
		DownloadURL: "https://raw.githubusercontent.com/o/hello/v1.1.0/plugin.go", SHA256: sum([]byte(helloSrc)),
		MinGoblogVersion: "0.2.6", InstallType: "dynamic", ReleasedAt: "2026-09-15T00:00:00Z",
		DetailURL: "https://example.test/plugins/plugins/hello.json",
	}
	if e != want {
		t.Errorf("entry =\n%+v\nwant\n%+v", e, want)
	}

	var d DetailDoc
	mustJSON(t, filepath.Join(out, "plugins", "hello.json"), &d)
	if d.Name != "hello" || d.ReadmeHTML != "<p># Hello</p>" || d.ChangelogHTML != "<p>## 1.1.0\n- second</p>" {
		t.Errorf("detail = %+v", d)
	}
	if len(d.Releases) != 2 || d.Releases[0].Version != "1.1.0" || d.Releases[0].NotesHTML != "<p>Second</p>" || d.Releases[0].ReleasedAt != "2026-09-15T00:00:00Z" || d.Releases[0].URL == "" || d.Releases[1].Version != "1.0.0" {
		t.Errorf("releases = %+v", d.Releases)
	}
	var z DetailDoc
	mustJSON(t, filepath.Join(out, "plugins", "zeta.json"), &z)
	if z.ChangelogHTML != "" {
		t.Errorf("missing CHANGELOG.md should give empty changelog_html, got %q", z.ChangelogHTML)
	}

	html, _ := os.ReadFile(filepath.Join(out, "index.html"))
	if !strings.Contains(string(html), "goblog.live/plugins") || !strings.Contains(string(html), "index.json") {
		t.Errorf("index.html should point readers at goblog.live/plugins and index.json, got %q", html)
	}
	if _, err := os.Stat(filepath.Join(out, ".nojekyll")); err != nil {
		t.Error(".nojekyll should exist so Pages serves files as-is")
	}
	// The raw index must be exactly what a consumer parses: check it round-trips byte-for-byte.
	raw, _ := os.ReadFile(filepath.Join(out, "index.json"))
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Errorf("index.json is not valid JSON: %v", err)
	}
}

func TestBuild_SkipsBrokenEntriesAndDuplicates(t *testing.T) {
	src := helloSource()
	// "o/copy" is a valid repo whose manifest reuses the name "hello".
	src.releases["o/copy"] = src.releases["o/hello"]
	for k, v := range src.files {
		if strings.HasPrefix(k, "o/hello@") {
			src.files[strings.Replace(k, "o/hello@", "o/copy@", 1)] = v
		}
	}
	out := t.TempDir()
	res, err := Build(context.Background(), src, helloValidator(), []string{"o/hello", "o/nope", "o/copy"}, out, "https://example.test/plugins")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Built) != 1 || res.Built[0] != "o/hello" {
		t.Errorf("built = %v", res.Built)
	}
	if len(res.Skipped) != 2 || res.Skipped["o/nope"] == nil || res.Skipped["o/copy"] == nil {
		t.Errorf("skipped = %v", res.Skipped)
	}
	if !strings.Contains(res.Skipped["o/copy"].Error(), "already") {
		t.Errorf("duplicate name error should say so: %v", res.Skipped["o/copy"])
	}
	var index []IndexEntry
	mustJSON(t, filepath.Join(out, "index.json"), &index)
	if len(index) != 1 {
		t.Errorf("index should contain only the good entry, got %d", len(index))
	}
	if _, err := os.Stat(filepath.Join(out, "plugins", "nope.json")); err == nil {
		t.Error("no detail file for a skipped entry")
	}
}

func TestBuild_FailsWhenNothingBuilt(t *testing.T) {
	src := helloSource()
	if _, err := Build(context.Background(), src, helloValidator(), []string{"o/nope"}, t.TempDir(), "https://example.test/plugins"); err == nil {
		t.Error("a build with zero valid entries must fail rather than publish an empty index")
	}
}

func mustJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/registry/ -run TestBuild -v`
Expected: FAIL to compile — `undefined: Build`, `IndexEntry`, `DetailDoc`.

- [ ] **Step 3: Implement**

`internal/registry/build.go`:

```go
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// IndexEntry is one element of index.json: the latest release of a plugin.
// Field names are the contract consumed by goblog's directory plugin and
// the admin installer; do not rename them.
type IndexEntry struct {
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
	ReleasedAt       string `json:"released_at"`
	DetailURL        string `json:"detail_url"`
}

// ReleaseDoc is one release in a plugin's history.
type ReleaseDoc struct {
	Version    string `json:"version"`
	ReleasedAt string `json:"released_at"`
	NotesHTML  string `json:"notes_html"`
	URL        string `json:"url"`
}

// DetailDoc is plugins/<name>.json: the index entry plus rendered README,
// changelog and release history.
type DetailDoc struct {
	IndexEntry
	ReadmeHTML    string       `json:"readme_html"`
	ChangelogHTML string       `json:"changelog_html"`
	Releases      []ReleaseDoc `json:"releases"`
}

// BuildResult says which repositories made it into the index.
type BuildResult struct {
	Built   []string
	Skipped map[string]error
}

const indexHTML = `<!doctype html>
<meta charset="utf-8">
<title>goblog plugin registry</title>
<p>This is the machine-readable goblog plugin index. Browse the directory at
<a href="https://goblog.live/plugins">goblog.live/plugins</a>, or fetch
<a href="index.json">index.json</a>. To publish a plugin, see
<a href="https://github.com/goblogplatform/plugins">goblogplatform/plugins</a>.</p>
`

// Build validates every repository and writes index.json, plugins/<name>.json
// and index.html under outDir. A repository that fails validation is
// skipped and reported in the result so one broken release cannot take the
// directory down; the build fails outright only when nothing is valid.
func Build(ctx context.Context, src Source, val Validator, repos []string, outDir, baseURL string) (BuildResult, error) {
	res := BuildResult{Skipped: map[string]error{}}
	baseURL = strings.TrimSuffix(baseURL, "/")
	var index []IndexEntry
	var details []DetailDoc
	byName := map[string]string{} // plugin name → repo that claimed it

	for _, repo := range repos {
		v, err := ValidateEntry(ctx, src, val, repo)
		if err != nil {
			log.Printf("skip %s: %v", repo, err)
			res.Skipped[repo] = err
			continue
		}
		if prev, taken := byName[v.Manifest.Name]; taken {
			err := fmt.Errorf("%s: plugin name %q is already published by %s", repo, v.Manifest.Name, prev)
			log.Printf("skip %s: %v", repo, err)
			res.Skipped[repo] = err
			continue
		}
		d, err := buildDetail(ctx, src, v, baseURL)
		if err != nil {
			log.Printf("skip %s: %v", repo, err)
			res.Skipped[repo] = err
			continue
		}
		byName[v.Manifest.Name] = repo
		index = append(index, d.IndexEntry)
		details = append(details, d)
		res.Built = append(res.Built, repo)
	}
	if len(index) == 0 {
		return res, fmt.Errorf("no valid plugins; refusing to publish an empty index")
	}
	sort.Slice(index, func(i, j int) bool { return index[i].Name < index[j].Name })

	if err := os.MkdirAll(filepath.Join(outDir, "plugins"), 0755); err != nil {
		return res, err
	}
	if err := writeJSON(filepath.Join(outDir, "index.json"), index); err != nil {
		return res, err
	}
	for _, d := range details {
		if err := writeJSON(filepath.Join(outDir, "plugins", d.Name+".json"), d); err != nil {
			return res, err
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(indexHTML), 0644); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(outDir, ".nojekyll"), nil, 0644); err != nil {
		return res, err
	}
	return res, nil
}

func buildDetail(ctx context.Context, src Source, v *Validated, baseURL string) (DetailDoc, error) {
	ownerRepo := v.Owner + "/" + v.Name
	entry := IndexEntry{
		Name:             v.Manifest.Name,
		DisplayName:      v.Manifest.DisplayName,
		Description:      v.Manifest.Description,
		Version:          v.Version,
		Author:           v.Manifest.Author,
		License:          v.Manifest.License,
		SourceURL:        "https://github.com/" + ownerRepo,
		DownloadURL:      fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", ownerRepo, v.Release.Tag, v.Manifest.Entry),
		SHA256:           v.SHA256,
		MinGoblogVersion: v.Manifest.MinGoblogVersion,
		InstallType:      "dynamic",
		ReleasedAt:       v.Release.PublishedAt.UTC().Format(time.RFC3339),
		DetailURL:        fmt.Sprintf("%s/plugins/%s.json", baseURL, v.Manifest.Name),
	}

	readme, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "README.md")
	if err != nil {
		return DetailDoc{}, fmt.Errorf("README.md: %w", err)
	}
	readmeHTML, err := src.RenderMarkdown(ctx, ownerRepo, string(readme))
	if err != nil {
		return DetailDoc{}, err
	}
	changelogHTML := ""
	if cl, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "CHANGELOG.md"); err == nil {
		if changelogHTML, err = src.RenderMarkdown(ctx, ownerRepo, string(cl)); err != nil {
			return DetailDoc{}, err
		}
	} else if !isNotFound(err) {
		return DetailDoc{}, fmt.Errorf("CHANGELOG.md: %w", err)
	}

	releases := make([]ReleaseDoc, 0, len(v.Releases))
	for _, r := range v.Releases {
		notes, err := src.RenderMarkdown(ctx, ownerRepo, r.Body)
		if err != nil {
			return DetailDoc{}, err
		}
		releases = append(releases, ReleaseDoc{
			Version:    strings.TrimPrefix(r.Tag, "v"),
			ReleasedAt: r.PublishedAt.UTC().Format(time.RFC3339),
			NotesHTML:  notes,
			URL:        r.URL,
		})
	}
	return DetailDoc{IndexEntry: entry, ReadmeHTML: readmeHTML, ChangelogHTML: changelogHTML, Releases: releases}, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
```

- [ ] **Step 4: Run tests, commit**

Run: `go test ./internal/registry/ -v && go vet ./...`
Expected: PASS for all.

```bash
git add -A && git commit -m "Build index.json and per-plugin detail documents from validated entries

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push
```

---

### Task 6: CLI, contract doc, README, Renovate

**Files:**
- Create: `cmd/registry/main.go`, `docs/CONTRACT.md`, `README.md`, `renovate.json`
- Test: `cmd/registry/main_test.go`

**Interfaces:**
- Consumes: `LoadRegistry`, `NewGitHubSource`, `NewDockerValidator`, `ValidateEntry`, `Build`, `BuildResult`.
- Produces: the `registry` binary with `validate` and `build` subcommands; exit codes 0 / 1 (fatal or validation failure) / 2 (build wrote output but skipped entries). `run(args []string, stdout, stderr io.Writer, src Source, val Validator) int` is the testable core; `main` wires real implementations from flags/env.

- [ ] **Step 1: Write the failing test**

`cmd/registry/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goblogplatform/plugins/internal/registry"
)

type memSource struct {
	releases map[string][]registry.Release
	files    map[string]string
}

func (m *memSource) Releases(_ context.Context, owner, repo string) ([]registry.Release, error) {
	if r, ok := m.releases[owner+"/"+repo]; ok {
		return r, nil
	}
	return nil, errors.New("no such repo")
}
func (m *memSource) File(_ context.Context, owner, repo, ref, path string) ([]byte, error) {
	if c, ok := m.files[owner+"/"+repo+"@"+ref+":"+path]; ok {
		return []byte(c), nil
	}
	return nil, registry.ErrNotFound
}
func (m *memSource) RenderMarkdown(_ context.Context, _, md string) (string, error) { return "<p>" + md + "</p>", nil }

type okValidator struct{}

func (okValidator) Validate(_ context.Context, _ []byte) (registry.Info, error) {
	return registry.Info{Name: "hello", DisplayName: "Hello", Version: "1.0.0"}, nil
}

func fixture(t *testing.T) (string, *memSource) {
	t.Helper()
	dir := t.TempDir()
	reg := filepath.Join(dir, "registry.yaml")
	os.WriteFile(reg, []byte("plugins:\n  - repo: o/hello\n  - repo: o/broken\n"), 0644)
	src := &memSource{
		releases: map[string][]registry.Release{
			"o/hello":  {{Tag: "v1.0.0", Body: "First", URL: "u", PublishedAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}},
			"o/broken": {},
		},
		files: map[string]string{
			"o/hello@v1.0.0:goblog-plugin.json": `{"name":"hello","display_name":"Hello","description":"d","author":"a","license":"MIT","min_goblog_version":"0.2.6"}`,
			"o/hello@v1.0.0:plugin.go":          "package main\n",
			"o/hello@v1.0.0:README.md":          "# Hello",
		},
	}
	return reg, src
}

func TestRun_Validate(t *testing.T) {
	reg, src := fixture(t)
	var out, errOut bytes.Buffer
	code := run([]string{"validate", "--registry", reg, "--repo", "o/hello"}, &out, &errOut, src, okValidator{})
	if code != 0 || !strings.Contains(out.String(), "o/hello: ok (hello 1.0.0)") {
		t.Errorf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = run([]string{"validate", "--registry", reg}, &out, &errOut, src, okValidator{})
	if code != 1 || !strings.Contains(errOut.String(), "o/broken") || !strings.Contains(out.String(), "o/hello: ok") {
		t.Errorf("all entries: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if code := run([]string{"validate", "--registry", reg, "--repo", "o/nothere"}, &out, &errOut, src, okValidator{}); code != 1 {
		t.Errorf("unknown --repo should fail, got %d", code)
	}
}

func TestRun_Build(t *testing.T) {
	reg, src := fixture(t)
	dist := filepath.Join(t.TempDir(), "dist")
	var out, errOut bytes.Buffer
	code := run([]string{"build", "--registry", reg, "--out", dist, "--base-url", "https://x.test/p"}, &out, &errOut, src, okValidator{})
	if code != 2 {
		t.Errorf("a build with a skipped entry should exit 2, got %d (err=%q)", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dist, "index.json")); err != nil {
		t.Error("index.json should still be written")
	}
	if !strings.Contains(errOut.String(), "o/broken") {
		t.Errorf("skipped entry should be reported on stderr, got %q", errOut.String())
	}
	os.WriteFile(reg, []byte("plugins:\n  - repo: o/hello\n"), 0644)
	if code := run([]string{"build", "--registry", reg, "--out", dist}, &out, &errOut, src, okValidator{}); code != 0 {
		t.Errorf("clean build should exit 0, got %d", code)
	}
}

func TestRun_Usage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut, nil, nil); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Errorf("code=%d err=%q", code, errOut.String())
	}
	if code := run([]string{"frobnicate"}, &out, &errOut, nil, nil); code != 2 {
		t.Errorf("unknown command: code=%d", code)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/registry/ -v`
Expected: FAIL to compile — `undefined: run`.

- [ ] **Step 3: Implement**

`cmd/registry/main.go`:

```go
// Command registry validates the plugin repositories listed in registry.yaml
// and builds the directory index published to GitHub Pages.
//
//	registry validate [--registry registry.yaml] [--image compscidr/goblog:vX.Y.Z] [--repo owner/name]
//	registry build    [--registry registry.yaml] [--image ...] [--out dist] [--base-url URL]
//
// GITHUB_TOKEN is used when set. Exit codes: 0 ok; 1 a validation failed or
// a fatal error; 2 (build only) output was written but some entries were skipped.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/goblogplatform/plugins/internal/registry"
)

const (
	defaultImage   = "compscidr/goblog:v0.2.7"
	defaultBaseURL = "https://goblogplatform.github.io/plugins"
)

func main() {
	// Flags are parsed twice: once here to build the real Source/Validator,
	// once in run for everything else. Keep the flag names in sync.
	image := defaultImage
	for i, a := range os.Args {
		if a == "--image" && i+1 < len(os.Args) {
			image = os.Args[i+1]
		}
	}
	src, err := registry.NewGitHubSource(os.Getenv("GITHUB_TOKEN"), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, src, registry.NewDockerValidator(image)))
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: registry validate [--registry FILE] [--image IMAGE] [--repo owner/name]")
	fmt.Fprintln(w, "       registry build    [--registry FILE] [--image IMAGE] [--out DIR] [--base-url URL]")
}

func run(args []string, stdout, stderr io.Writer, src registry.Source, val registry.Validator) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	regPath := fs.String("registry", "registry.yaml", "path to registry.yaml")
	fs.String("image", defaultImage, "goblog image used to load plugins (read in main)")
	repo := fs.String("repo", "", "validate only this owner/name (must be listed)")
	out := fs.String("out", "dist", "build output directory")
	baseURL := fs.String("base-url", defaultBaseURL, "public URL the output is served from")

	switch args[0] {
	case "validate", "build":
	default:
		usage(stderr)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	repos, err := registry.LoadRegistry(*regPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()

	switch args[0] {
	case "validate":
		if *repo != "" {
			found := false
			for _, r := range repos {
				found = found || r == *repo
			}
			if !found {
				fmt.Fprintf(stderr, "%s is not listed in %s\n", *repo, *regPath)
				return 1
			}
			repos = []string{*repo}
		}
		failed := 0
		for _, r := range repos {
			v, err := registry.ValidateEntry(ctx, src, val, r)
			if err != nil {
				fmt.Fprintf(stderr, "%s: FAIL: %v\n", r, err)
				failed++
				continue
			}
			fmt.Fprintf(stdout, "%s: ok (%s %s)\n", r, v.Manifest.Name, v.Version)
		}
		if failed > 0 {
			return 1
		}
		return 0

	case "build":
		res, err := registry.Build(ctx, src, val, repos, *out, *baseURL)
		for _, r := range res.Built {
			fmt.Fprintf(stdout, "%s: built\n", r)
		}
		skipped := make([]string, 0, len(res.Skipped))
		for r := range res.Skipped {
			skipped = append(skipped, r)
		}
		sort.Strings(skipped)
		for _, r := range skipped {
			fmt.Fprintf(stderr, "%s: SKIPPED: %v\n", r, res.Skipped[r])
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(skipped) > 0 {
			return 2
		}
		return 0
	}
	return 2
}
```

`docs/CONTRACT.md`:

````markdown
# Publishing a goblog plugin

The directory at [goblog.live/plugins](https://goblog.live/plugins) lists plugins from this registry. A plugin is a GitHub repository; each GitHub release is a version. Submitting means adding your repository to `registry.yaml` in a pull request — CI validates it and, once merged, the index is rebuilt (on every merge and every six hours).

## What the repository must contain

At the root, at every release tag:

| File | Required | Notes |
|---|---|---|
| `goblog-plugin.json` | yes | the manifest, below |
| the entry file (default `plugin.go`) | yes | a [dynamic plugin](https://github.com/goblogplatform/goblog#dynamic-plugins): `package main`, `func NewPlugin() plugin.Plugin` |
| `README.md` | yes | shown on the plugin's directory page |
| `CHANGELOG.md` | no | shown when present |
| `LICENSE` | recommended | should match `license` in the manifest |

### `goblog-plugin.json`

```json
{
  "name": "hello",
  "display_name": "Hello",
  "description": "One sentence shown in the listing.",
  "author": "Your Name",
  "license": "Apache-2.0",
  "entry": "plugin.go",
  "min_goblog_version": "0.2.6",
  "homepage": "https://example.com/optional"
}
```

- `name`: `^[a-z0-9-]+$`, unique across the registry, and equal to what your plugin's `Name()` returns.
- `license`: an SPDX identifier from the list in `internal/registry/manifest.go` (MIT, Apache-2.0, BSD-2/3-Clause, ISC, MPL-2.0, GPL/LGPL/AGPL `-only`/`-or-later`, Unlicense, 0BSD). Open an issue to add another.
- `entry`: a `.go` file at the repository root; defaults to `plugin.go`.
- `min_goblog_version`: plain semver (`0.2.6`, no `v`) — the oldest goblog your plugin works with.

### Releases

- Tag releases `vX.Y.Z` (exactly three numbers). Drafts and pre-releases are ignored.
- The tag without `v` must equal the string your plugin's `Version()` returns.
- The GitHub release body is shown as the version's release notes.
- The directory lists the **latest** published release; the detail page shows all of them.

## Check before you submit

```bash
docker run --rm --network none -v "$PWD:/p:ro" \
  --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:v0.2.7 \
  validate-plugin /p/plugin.go
# {"name":"hello","display_name":"Hello","version":"1.0.0"}
```

The registry's CI runs exactly this, then compares `name` and `version` with your manifest and tag. Your file is executed by the Go interpreter during the check, which is why it runs with networking off.

## Submit

1. Fork this repository and add a line to `registry.yaml`:
   ```yaml
   plugins:
     - repo: goblogplatform/goblog-plugin-hello
     - repo: you/goblog-plugin-yours
   ```
2. Open a pull request. The `validate` workflow must pass.
3. After merge, `https://goblogplatform.github.io/plugins/index.json` and goblog.live/plugins pick it up within a few minutes. New releases of your plugin are picked up automatically on the next scheduled build.

Plugins run inside the goblog process of whoever installs them. Keep them small and readable; the registry is curated and maintainers may decline or remove entries.
````

`README.md` (registry root):

````markdown
# goblog plugin registry

The curated list of [goblog](https://github.com/goblogplatform/goblog) plugins behind [goblog.live/plugins](https://goblog.live/plugins).

- `registry.yaml` — the list. Add your repository in a PR; see [docs/CONTRACT.md](docs/CONTRACT.md).
- `https://goblogplatform.github.io/plugins/index.json` — the machine-readable index (latest release of each plugin, with `download_url` and `sha256`); `plugins/<name>.json` adds the rendered README, changelog and release history.
- `cmd/registry` — the tool CI runs: `validate` on pull requests, `build` on merge and every six hours.

```bash
go run ./cmd/registry validate                      # every entry
go run ./cmd/registry validate --repo you/plugin    # one entry
go run ./cmd/registry build --out dist              # what gets published
```

Set `GITHUB_TOKEN` to avoid API rate limits. `validate`/`build` run `goblog validate-plugin` in the `compscidr/goblog` Docker image (`--image` to override; Renovate keeps the default current).

License: Apache-2.0.
````

`renovate.json`:

```json
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "extends": ["config:recommended"],
  "packageRules": [
    { "matchUpdateTypes": ["minor", "patch"], "automerge": true }
  ],
  "customManagers": [
    {
      "customType": "regex",
      "description": "Pin of the goblog image used to validate plugins, in workflows and code",
      "managerFilePatterns": ["/^\\.github/workflows/.*\\.ya?ml$/", "/^cmd/registry/main\\.go$/", "/^docs/CONTRACT\\.md$/"],
      "matchStrings": ["compscidr/goblog:(?<currentValue>v\\d+\\.\\d+\\.\\d+)"],
      "depNameTemplate": "compscidr/goblog",
      "datasourceTemplate": "docker"
    }
  ]
}
```

- [ ] **Step 4: Run tests, try the real thing, commit**

Run: `go test ./... && go vet ./... && go build ./...`
Expected: PASS.

Real run against GitHub (needs Docker and the `v0.2.7` image):
```bash
cd ~/dev/goblog-plugins && GITHUB_TOKEN=$(gh auth token) go run ./cmd/registry validate
GITHUB_TOKEN=$(gh auth token) go run ./cmd/registry build --out dist && cat dist/index.json && ls dist/plugins
```
Expected: `goblogplatform/goblog-plugin-hello: ok (hello 1.0.0)`; `dist/index.json` with one entry whose `sha256` equals `sha256sum ~/dev/goblog-plugin-hello/plugin.go`; `dist/plugins/hello.json` with `readme_html` starting with `<h1`. `dist/` is gitignored.

```bash
git add -A && git commit -m "Add the registry CLI, contributor contract, README and Renovate config

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push
```

---

### Task 7: Workflows, GitHub Pages, go-live

**Files:**
- Create: `.github/workflows/validate.yml`, `.github/workflows/publish.yml`

**Interfaces:**
- Consumes: the `registry` CLI and its exit codes (Task 6).
- Produces: `https://goblogplatform.github.io/plugins/index.json` live.

- [ ] **Step 1: Write the workflows**

`.github/workflows/validate.yml`:

```yaml
name: Validate
on:
  pull_request:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - name: Unit tests
        run: go test ./...
      - name: Validate every registry entry
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: go run ./cmd/registry validate --image compscidr/goblog:v0.2.7
```

`.github/workflows/publish.yml`:

```yaml
name: Publish index
on:
  push:
    branches: [main]
  schedule:
    - cron: "0 */6 * * *"
  workflow_dispatch:
permissions:
  contents: read
  pages: write
  id-token: write
concurrency:
  group: pages
  cancel-in-progress: false
jobs:
  build:
    runs-on: ubuntu-latest
    outputs:
      skipped: ${{ steps.build.outputs.skipped }}
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - name: Build the index
        id: build
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          set +e
          go run ./cmd/registry build --out dist --image compscidr/goblog:v0.2.7
          code=$?
          set -e
          if [ "$code" = "2" ]; then echo "skipped=true" >> "$GITHUB_OUTPUT"; exit 0; fi
          exit $code
      - uses: actions/upload-pages-artifact@v4
        with:
          path: dist
  deploy:
    needs: build
    runs-on: ubuntu-latest
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - id: deployment
        uses: actions/deploy-pages@v4
  report-skipped:
    needs: [build, deploy]
    if: needs.build.outputs.skipped == 'true'
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "::error::Some registry entries were skipped; see the build job log."
          exit 1
```

Check the current major versions of `actions/checkout`, `actions/setup-go`, `actions/upload-pages-artifact`, `actions/deploy-pages` with `gh api repos/actions/<name>/releases/latest --jq .tag_name` and use the latest majors if the ones above are stale.

- [ ] **Step 2: Enable Pages, commit, push, watch**

```bash
cd ~/dev/goblog-plugins
gh api -X POST repos/goblogplatform/plugins/pages -f build_type=workflow 2>&1 | tail -1   # 201, or 409 if already enabled
git add -A && git commit -m "Validate registry entries on PRs and publish the index to GitHub Pages

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push
gh run watch --exit-status $(gh run list --workflow "Publish index" --limit 1 --json databaseId --jq '.[0].databaseId')
```
Expected: the `Publish index` run succeeds with `report-skipped` skipped.

- [ ] **Step 3: Verify the live index**

```bash
curl -fsS https://goblogplatform.github.io/plugins/index.json | python3 -m json.tool | head -20
curl -fsS https://goblogplatform.github.io/plugins/plugins/hello.json | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["name"], d["version"], d["readme_html"][:40], len(d["releases"]))'
curl -fsS -o /dev/null -w '%{http_code}\n' https://goblogplatform.github.io/plugins/
```
Expected: one entry `hello` 1.0.0 with `download_url` `https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go`; detail with `<h1` README and 1 release; `200` for the HTML page. Pages can take a minute after the first deploy — retry a few times before concluding it failed.

Also confirm the sha in the index matches the file: `curl -fsS $(curl -fsS https://goblogplatform.github.io/plugins/index.json | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["download_url"])') | sha256sum` equals the index's `sha256`.

- [ ] **Step 4: Prove the PR path**

```bash
cd ~/dev/goblog-plugins && git checkout -b test/validate-workflow
printf '  - repo: goblogplatform/goblog\n' >> registry.yaml   # goblog itself has releases but no manifest → must fail validation
git commit -am "Test: a repo without a manifest must fail validation" && git push -u origin test/validate-workflow
gh pr create --fill --draft
gh pr checks --watch || true
```
Expected: the `Validate` check **fails** with `goblogplatform/goblog: FAIL: ... goblog-plugin.json: ... not found` and the good entry still says `ok`. Then close the PR and delete the branch:
```bash
gh pr close --delete-branch "$(gh pr view --json number --jq .number)"
git checkout main && git branch -D test/validate-workflow
```

---

### Task 8: goblog docs PR and issue close-out

**Files (in `~/dev/goblog`, branch `docs/552-piece-b`):**
- Already committed there: the spec addendum and this plan.
- Modify: `README.md` "### Plugin directory" paragraph — no text change needed unless a URL in it is wrong; verify the two links (`goblog.live/plugins`, `goblogplatform/plugins`) resolve.

- [ ] **Step 1: Open the docs PR**

```bash
cd ~/dev/goblog && git branch --show-current   # docs/552-piece-b
git push -u origin docs/552-piece-b
gh pr create --base main --title "Docs: plugin registry layout and plan (#552)" --body "$(cat <<'EOF'
Spec addendum and implementation plan for piece B of #552 (the registry and seed plugin repos, now live at https://github.com/goblogplatform/plugins and https://github.com/goblogplatform/goblog-plugin-hello; index at https://goblogplatform.github.io/plugins/index.json).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 2: Report what is left for a human**

Not automatable from here; list in the final report with exact steps:
1. Deploy goblog v0.2.7 to goblog.live: merge the Renovate bump in `~/dev/iac` (or edit `ansible/roles/projects/tasks/main.yml` `image: compscidr/goblog:v0.2.7`), then `cd ~/dev/iac/ansible && ansible-playbook -i inventory.yml projects.yml --limit projects --tags goblog-live`.
2. On goblog.live: Admin → Settings → Plugin Directory → `enabled` = `true` (leave `index_url` at its default). Check https://goblog.live/plugins and https://goblog.live/plugins/index.json.
3. Close #552 with a comment linking the registry repo, the Pages index, and noting for #553 that the index URL is `https://goblog.live/plugins/index.json`.
4. Open the follow-up issue: "`Registry.Init` stops at the first plugin whose `OnInit` fails; later plugins (all dynamic ones) never get settings seeded".

---

## Self-review notes

- Spec coverage: contract (Task 1 example repo + `docs/CONTRACT.md` in Task 6), manifest checks (Task 2), registry list + GitHub source (Task 3), Docker validator + identity checks (Task 4), index/detail/index.html + skip semantics + duplicate names + exit codes (Tasks 5–6), workflows + Pages + Renovate (Tasks 6–7), go-live steps (Task 8).
- Type consistency: `Source`/`Release`/`ErrNotFound` (Task 3) used unchanged in Tasks 4–6; `Validated` fields used by `buildDetail` match Task 4; `FakeValidator`/`memSource`/`helloSource`/`helloValidator`/`sum`/`goodManifest` are defined in Task 2/4 test files and reused in Task 5 (same package). `cmd/registry` tests define their own `memSource` because they are in package `main`.
- The `--image` flag is read in `main` (to construct the real validator) and also declared in `run` so `flag` accepts it; `run` ignores its value.
