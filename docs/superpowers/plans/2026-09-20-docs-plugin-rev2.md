# Documentation as a WASM plugin (rev 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Take the documentation out of the goblog binary: the pages stay in the goblog repo (with their CI lint) and are served on goblog.live by a separate WASM plugin, `goblogplatform/goblog-plugin-docs`.

**Architecture:** goblog keeps `docs/guide/*.md` (the seven pages) and a markdown-level test package that checks H1s, links/anchors and identifiers against the code — no renderer, no goldmark. A new plugin repo vendors the pages (`scripts/sync-content.sh <goblog-ref>`), renders them with goldmark inside the WASM sandbox, and returns the `html` result form so goblog wraps it in the theme's `page_content.html`. Existing branch `feat/docs-plugin` (PR #591, head 076ad07) is reworked in place: everything under `plugins/docs/` moves out; the registry fix, README trims, contract pointers and footer links stay.

**Tech Stack:** goblog: Go 1.25, stdlib only for the tests. Plugin: Go 1.25 `wasip1`, `github.com/extism/go-pdk`, `github.com/yuin/goldmark`, `html/template`.

**Spec:** `docs/superpowers/specs/2026-09-20-docs-plugin-design.md` (rev 2 section added in Task 1). Supersedes `2026-09-20-docs-plugin.md` for Tasks 1–2, 7–8 of that plan.

## Global Constraints

- Seven pages, unchanged content, same slugs/titles: `` Overview `overview.md`; `writing-a-plugin` Writing a plugin; `plugin-api` Plugin API reference; `publishing-a-plugin` Publishing a plugin; `writing-a-theme` Writing a theme; `publishing-a-theme` Publishing a theme; `directory-formats` Directory formats. Each starts with an H1 equal to its title. Internal links `/docs/<slug>[#anchor]` / `/docs#anchor` / `#anchor`.
- goblog after Task 1: no `plugins/docs` package, no goldmark in `go.mod`, `goblog.go` does not register a docs plugin; `plugin/wasm.Exports`, `HostFunctions`, `ContractFieldNames`, `registry.KnownLicenses` remain (the lint uses them); the `ensurePages` post-type fix and its tests remain; `gofmt`/`go vet ./docs/guide/`/`go test ./...` green.
- Plugin: name `docs`, display name "Documentation", version `1.0.0`, page type `docs`, slug `docs`, title "Docs", nav order 32, `show_in_nav` true, one setting `enabled` default `"false"`; `allowed_hosts: []`; `min_goblog_version` `0.5.1`; Apache-2.0; `render_page` returns `{"html": …}` for `""` and known slugs, `{}` (decline) otherwise; layout identical to the compiled-in version (`row g-4 docs`, sidebar with `aria-current="page"`, `docs-toc` when > 3 H2/H3, scoped table/pre CSS).
- Commits end with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Never push to `main`; never merge.

## File structure

```
goblog (worktree .claude/worktrees/docs-plugin, branch feat/docs-plugin)
  docs/guide/*.md                        MOVE from plugins/docs/content/
  docs/guide/doc.go, guide_test.go       NEW — markdown-level checks (embed *.md)
  docs/PLUGIN_CONTRACT.md, THEME_CONTRACT.md   pointers → guide/…
  plugins/docs/                          DELETE
  goblog.go, go.mod, go.sum              remove registration + goldmark
  README.md                              feature bullet + "source of those pages" sentence
  docs/superpowers/specs/…docs-plugin-design.md   rev 2 section
  docs/superpowers/plans/2026-09-20-docs-plugin.md  superseded note

~/dev/goblog-plugin-docs (new repo)
  main.go            //go:build wasip1 — exports identity/settings/pages/render_page
  render.go          goldmark render + TOC + layout (no build tag; native-testable)
  pages.go           manifest (slug/title/file)
  content/*.md, content/GOBLOG_REF       vendored by scripts/sync-content.sh
  templates/page.html                    layout (embedded)
  render_test.go, main_native.go         native tests
  scripts/sync-content.sh
  goblog-plugin.json, go.mod, README.md, CHANGELOG.md, LICENSE, .gitignore
  .github/workflows/release.yml          copied from goblog-plugin-scholar
```

---

### Task 1: goblog — pages to `docs/guide/`, markdown-level tests, remove the compiled-in plugin

**Files:**
- Move: `plugins/docs/content/*.md` → `docs/guide/*.md` (`git mv`)
- Create: `docs/guide/doc.go`, `docs/guide/guide_test.go`
- Delete: `plugins/docs/` (everything else in it)
- Modify: `goblog.go` (drop `"goblog/plugins/docs"` import and `registry.Register(docs.New())`), `go.mod`/`go.sum` (`go mod tidy` removes goldmark), `docs/PLUGIN_CONTRACT.md`, `docs/THEME_CONTRACT.md`, `README.md`, `docs/superpowers/specs/2026-09-20-docs-plugin-design.md`, `docs/superpowers/plans/2026-09-20-docs-plugin.md`

**Interfaces:**
- Produces: `docs/guide` package (`package guide`, `//go:embed *.md` as `Content embed.FS`, `var Pages = []Page{{Slug, Title, File}…}`) — exported so the plugin repo's sync script has a manifest to copy and so tests are readable; nothing in goblog imports it.

- [ ] **Step 1: Move the content and delete the plugin**

```bash
git mv plugins/docs/content docs/guide
git rm -r -q plugins/docs
```
Edit `goblog.go`: remove the docs import and the `registry.Register(docs.New())` line (keep `directory`). `go mod tidy` → goldmark lines disappear from `go.mod`/`go.sum`. `go build ./...` passes.

- [ ] **Step 2: `docs/guide/doc.go`**

```go
// Package guide holds goblog's builder documentation — how to write and
// publish plugins and themes — as markdown. It is not served by goblog:
// goblogplatform/goblog-plugin-docs vendors these files and renders them at
// /docs on goblog.live. The tests here keep the pages honest against the
// code (links resolve, every identifier they name exists).
package guide

import "embed"

// Content is every page, by file name.
//
//go:embed *.md
var Content embed.FS

// Page is one documentation page: its URL segment under /docs, the title
// (also its H1), and its file.
type Page struct {
	Slug, Title, File string
}

// Pages is the sidebar order. Slug "" is the index.
var Pages = []Page{
	{"", "Overview", "overview.md"},
	{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
	{"plugin-api", "Plugin API reference", "plugin-api.md"},
	{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
	{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
	{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
	{"directory-formats", "Directory formats", "directory-formats.md"},
}
```

- [ ] **Step 3: `docs/guide/guide_test.go` — port the checks to markdown**

Port `plugins/docs/content_test.go` (at 076ad07) and the H1 check from `docs_test.go`, replacing rendered-HTML matching with markdown matching:

```go
package guide

import (
	"bufio"
	"bytes"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"goblog/plugin/wasm"
	"goblog/plugins/directory/registry"
)

func read(t *testing.T, file string) []byte {
	t.Helper()
	b, err := Content.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// headingID mirrors goldmark's parser.WithAutoHeadingID(): lowercase,
// alphanumerics and '-' kept, '_' and spaces become '-', everything else
// dropped; a repeated ID gets -1, -2, … appended. The plugin's own tests
// check the same anchors with real goldmark, so a divergence here fails
// on that side, never silently.
func headingIDs(src []byte) map[string]bool {
	ids := map[string]bool{}
	seen := map[string]int{}
	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimLeft(line, "#")
		if !strings.HasPrefix(text, " ") {
			continue
		}
		text = strings.TrimSpace(strings.ReplaceAll(text, "`", ""))
		var b strings.Builder
		for _, r := range strings.ToLower(text) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
				b.WriteRune(r)
			case r == ' ', r == '_':
				b.WriteByte('-')
			}
		}
		id := b.String()
		if n := seen[id]; n > 0 {
			id += "-" + strconv.Itoa(n) // goldmark: second "foo" is "foo-1"
		}
		seen[b.String()]++
		ids[id] = true
	}
	return ids
}
```

(add `strconv` to the imports.)

Tests to write, each asserting on the markdown:

- `TestPagesHaveTitleH1`: every `Pages[i].File` exists in `Content` and its first non-empty line is `"# " + Title`.
- `TestInternalLinksResolve`: regex `\]\((/docs(?:/([a-z0-9-]+))?(?:#([A-Za-z0-9_-]+))?)\)` over each page — slug must be in `Pages`; anchor (if any) must be in `headingIDs(target)`. Same-page `\]\(#([A-Za-z0-9_-]+)\)` must be in the page's own IDs. Skip matches inside fenced code blocks (strip fences first with the same scanner logic, or accept that no page links inside a fence — assert that by stripping).
- `TestNoAbsoluteSelfLinks`: no `goblog.live/docs` in any page.
- `TestSnakeCaseIdentifiersExist`: regex over backtick spans `` `(?:[a-z][a-z0-9_]*\.)*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)` `` (outside fences) against `knownIdentifiers()` — port `knownIdentifiers`, `jsonTags` and the grouped allowlist verbatim from `plugins/docs/content_test.go` at 076ad07; and `plugin-api.md` must mention every `wasm.Exports` and `wasm.HostFunctions` entry in backticks.
- `TestLicenseListsMatchRegistry`: `writing-a-plugin.md`, `publishing-a-plugin.md`, `publishing-a-theme.md` each contain `` `<L>` `` for every `registry.KnownLicenses()`.

Run `go test ./docs/guide/ -v` — all must pass on the moved content without editing a page. If an anchor check fails only because `headingID` disagrees with goldmark for one heading, fix `headingID`, not the page (goldmark is the reference; the compiled-in tests passed on these pages at 076ad07, so every link resolved under real goldmark).

- [ ] **Step 4: Pointers, README, spec**

- `docs/PLUGIN_CONTRACT.md` / `THEME_CONTRACT.md`: source link → `guide/publishing-a-plugin.md` / `guide/publishing-a-theme.md` (keep the goblog.live link).
- `README.md`: remove the "Built-in documentation plugin (`docs`)" feature bullet; the §WebAssembly plugins pointer sentence becomes "… (or `/docs/plugin-api` on any goblog running the [docs plugin](https://github.com/goblogplatform/goblog-plugin-docs)); the source of those pages is `docs/guide/`."
- Spec: append `## Revision 2 (2026-09-20): served by a WASM plugin` — three paragraphs: why (goblog.live-specific serving code does not belong in every install), what moved where (pages + lint stay in goblog `docs/guide/`; rendering in `goblogplatform/goblog-plugin-docs`, vendored content, `html` result form), rollout. Plan file: add a line under the header: "> Superseded for Tasks 1–2, 7–8 by `2026-09-20-docs-plugin-rev2.md`; the content tasks (3–6) stand."

- [ ] **Step 5: Verify and commit**

`gofmt -l docs/guide plugin plugins` (only pre-existing plugin/plugin.go, plugin/validate_test.go noise) · `go vet ./docs/guide/` · `go build ./... && go test ./...` · `grep -rn "plugins/docs\|goldmark" --include=*.go --include=*.mod . | grep -v superpowers` → empty.

```bash
git add -A docs/guide plugins/docs goblog.go go.mod go.sum docs/PLUGIN_CONTRACT.md docs/THEME_CONTRACT.md README.md docs/superpowers
git commit -m "Docs move out of the binary: pages + lint stay in docs/guide, serving goes to goblog-plugin-docs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `goblog-plugin-docs` — the WASM plugin

**Files (new repo at `~/dev/goblog-plugin-docs`, `git init`, branch `main`):**
- `go.mod` (`module github.com/goblogplatform/goblog-plugin-docs`, `go 1.25.0`), `go.sum`
- `main.go`, `main_native.go`, `render.go`, `pages.go`, `templates/page.html`, `render_test.go`
- `scripts/sync-content.sh`, `content/*.md`, `content/GOBLOG_REF`
- `goblog-plugin.json`, `README.md`, `CHANGELOG.md`, `LICENSE` (Apache-2.0, copy from `~/dev/goblog-plugin-scholar/LICENSE`), `.gitignore` (`plugin.wasm`, `*.test`), `.github/workflows/release.yml` (copy verbatim from `~/dev/goblog-plugin-scholar/.github/workflows/release.yml`)

**Reference:** the compiled-in implementation at goblog commit 076ad07 — `git -C ~/dev/goblog/.claude/worktrees/docs-plugin show 076ad07:plugins/docs/render.go` (likewise `docs.go`, `templates/page.html`, `docs_test.go`, `content_test.go`). Port, don't redesign. The scholar plugin (`~/dev/goblog-plugin-scholar/main.go`) is the shape for exports, `hookInput`, `outputJSON`, `main_native.go`.

- [ ] **Step 1: `scripts/sync-content.sh`**

```bash
#!/usr/bin/env sh
# Vendors goblog's docs/guide pages at a ref into content/.
# Usage: scripts/sync-content.sh <goblog-ref> [path-to-local-goblog-checkout]
# With a local path the files are read with `git show <ref>:docs/guide/<f>`;
# otherwise they are fetched from raw.githubusercontent.com.
set -eu
ref=${1:?goblog ref (tag, branch or commit)}
local=${2:-}
pages="overview.md writing-a-plugin.md plugin-api.md publishing-a-plugin.md writing-a-theme.md publishing-a-theme.md directory-formats.md"
mkdir -p content
for f in $pages; do
  if [ -n "$local" ]; then
    git -C "$local" show "$ref:docs/guide/$f" > "content/$f"
  else
    curl -fsSL --max-time 30 "https://raw.githubusercontent.com/goblogplatform/goblog/$ref/docs/guide/$f" > "content/$f"
  fi
done
printf '%s\n' "$ref" > content/GOBLOG_REF
echo "synced $(echo $pages | wc -w) pages from goblog@$ref"
```

Run it once against the local worktree: `scripts/sync-content.sh feat/docs-plugin ~/dev/goblog/.claude/worktrees/docs-plugin` (after Task 1 is committed there). The page list must equal `pages.go`; a test asserts it (`TestSyncScriptListsEveryPage`: read the script, every `Pages[i].File` appears in it).

- [ ] **Step 2: `pages.go`, `render.go`, `templates/page.html`**

`pages.go`: the `page` struct and `pages` slice from goblog's `plugins/docs/pages.go` (same seven entries).

`render.go` (no build tag): port `Render`, `Heading`, `plainText` from goblog's `render.go` and `renderedPage`, `sidebarAndArticle`, `basePath`-equivalent and the embedded template from `docs.go`, minus gin/gorm:

```go
//go:embed content/*.md
var contentFS embed.FS

//go:embed templates/page.html
var pageTemplateSrc string

// site is the rendered documentation: every page, rendered once per module
// instance (goblog keeps one instance per plugin, so this happens once per
// process start, not per request).
type site struct{ bySlug map[string]renderedPage }

func newSite() (*site, error)                                  // renders every page; error names the file
func (s *site) render(basePath, slug string) (string, bool)     // sidebar + TOC + article HTML, false for unknown slug
```

`basePath` for the sidebar is the first segment of `request.path` (e.g. `/docs`), so links follow a renamed page slug — same rule as before.

`templates/page.html`: copy verbatim from goblog 076ad07 (includes the scoped `<style>`).

- [ ] **Step 3: `main.go` (wasip1) and `main_native.go`**

```go
//go:build wasip1

// Documentation is a goblog WebAssembly plugin: it serves goblog's builder
// docs (how to write and publish plugins and themes) at /docs, rendered
// from markdown vendored from goblog's docs/guide at the ref in
// content/GOBLOG_REF.
//
// Build:  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
package main

import (
	"encoding/json"

	pdk "github.com/extism/go-pdk"
)

type setting struct { Key, Type, Default, Label, Description string `json:"…"` }   // json tags: key,type,default,label,description
type hookInput struct {
	Settings map[string]string `json:"settings"`
	Request  struct {
		Path    string `json:"path"`
		SubPath string `json:"sub_path"`
	} `json:"request"`
}

var docs *site   // lazily built on first render_page; nil until then

func outputJSON(v any) int32 { … as scholar … }

//go:wasmexport identity
func identity() int32 { return outputJSON(map[string]string{"name": "docs", "display_name": "Documentation", "version": "1.0.0"}) }

//go:wasmexport settings
func settings() int32 {
	return outputJSON([]setting{{Key: "enabled", Type: "text", Default: "false", Label: "Enabled", Description: "Set to 'true' to publish the plugin and theme documentation at /docs"}})
}

//go:wasmexport pages
func pages() int32 {
	return outputJSON([]map[string]any{{"page_type": "docs", "title": "Docs", "slug": "docs", "show_in_nav": true, "nav_order": 32, "description": "How to build and publish goblog plugins and themes"}})
}

//go:wasmexport render_page
func renderPage() int32 {
	var in hookInput
	if err := json.Unmarshal(pdk.Input(), &in); err != nil { pdk.SetErrorString("render_page: " + err.Error()); return 1 }
	if docs == nil {
		s, err := newSite()
		if err != nil { pdk.SetErrorString("render_page: " + err.Error()); return 1 }
		docs = s
	}
	html, ok := docs.render(basePath(in.Request.Path), in.Request.SubPath)
	if !ok { return outputJSON(map[string]any{}) } // unknown sub-path: goblog 404s
	return outputJSON(map[string]any{"html": html})
}

func main() {}
```

`main_native.go`: `//go:build !wasip1` + `package main` + `func main() {}` (as scholar).

- [ ] **Step 4: `render_test.go` (native)**

Port from goblog's `docs_test.go`/`content_test.go` the parts that concern rendering: every page renders with `<h1 id="…">Title</h1>`; `newSite()` has all seven slugs; `render("/docs", "")` has the sidebar with `aria-current="page"` on Overview and links to every page; `render("/manual", "plugin-api")` links `/manual/writing-a-theme` and marks plugin-api current; unknown slugs (`nope`, `plugin-api/`, `../x`, `overview.md`) → `false`; TOC present iff `len(TOC) > 3` for every page plus the synthetic three-heading case; every `href="/docs…"` and `href="#…"` in rendered HTML resolves to an `id="…"` on the target page (real goldmark — this is the cross-check for goblog's `headingID`); `TestSyncScriptListsEveryPage`. No identifier lint here (it lives in goblog).

`go test ./...` (native) green; `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .` succeeds; note the size (goldmark + html/template — expect a few MB, must be < 16 MiB).

- [ ] **Step 5: Manifest, docs, workflow**

`goblog-plugin.json`:
```json
{
  "name": "docs",
  "display_name": "Documentation",
  "description": "goblog's builder documentation — how to write and publish plugins and themes — served at /docs.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "runtime": "wasm",
  "entry": "plugin.wasm",
  "allowed_hosts": [],
  "min_goblog_version": "0.5.1",
  "homepage": "https://github.com/goblogplatform/goblog-plugin-docs"
}
```
`README.md`: what it is, that content is vendored from goblog `docs/guide` at `content/GOBLOG_REF`, how to update (`scripts/sync-content.sh vX.Y.Z`, commit, tag), install from the directory (Admin → Plugins → Install → enable), build/test commands. `CHANGELOG.md`: `## 1.0.0 — first release; documents goblog <ref>`. `.gitignore`, `LICENSE`, `.github/workflows/release.yml` copied from scholar.

- [ ] **Step 6: Validate and smoke-test against goblog**

```bash
cd ~/dev/goblog/.claude/worktrees/docs-plugin && go run . validate-plugin ~/dev/goblog-plugin-docs/plugin.wasm
```
must print `{"name":"docs","display_name":"Documentation","version":"1.0.0","runtime":"wasm"}`. Then the Task-8 smoke flow from the first plan (build goblog, scratch sqlite DB, wizard/enabled settings via sqlite as that report recorded), with `plugin.wasm` copied to `plugins/wasm/docs.wasm` under the run directory and `ENABLE_WASM_PLUGINS` default: `curl -s localhost:7000/docs` shows the sidebar and `<h1 id="overview">Overview</h1>`; `/docs/plugin-api` contains `docs-toc`; `/docs/nope` → 404; server log shows no plugin errors. Kill by PID; remove the copied wasm.

- [ ] **Step 7: Commit**

```bash
cd ~/dev/goblog-plugin-docs && git add -A && git commit -m "Documentation plugin: serve goblog's docs/guide pages at /docs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
No remote yet — the controller creates the GitHub repo and pushes.

---

## Self-review

- Spec rev 2 coverage: pages + lint in goblog (T1); serving in the plugin with vendored content, `html` form, identical layout (T2); rollout is controller/user work (repo creation, force-push of #591, release, sync at tag, plugin v1.0.0, directory submission, enable).
- Placeholders: none; every code step is concrete.
- Type consistency: `Pages`/`Page` (goblog `guide`) vs `pages`/`page` (plugin) are deliberately separate — different modules; the sync script is the bridge and a test on each side checks the seven file names.
