# Plugin directory on goblog.live/plugins — design

Issue: goblogplatform/goblog#552. Related: #551 (plugin docs, done), #553 (admin
install/update, consumes the index), #555 (gin/gorm symbols for dynamic plugins),
#560 (theme directory, same shape).

## Goal

A browsable, curated directory of goblog plugins at `https://goblog.live/plugins`,
plus a machine-readable index at `https://goblog.live/plugins/index.json` that the
admin UI (#553) can search and install from.

## Decisions

- **Plugins are individual GitHub repos.** GitHub releases (`vX.Y.Z` tags) are the
  versions; the directory imports README, changelog and release notes from the repo.
  (Ansible Galaxy / WordPress model.)
- **A manifest `goblog-plugin.json`** at the repo root carries what the code can't
  (author, license, description, min goblog version, entry file).
- **The index lists the latest release only.** `download_url` is pinned to the
  release tag and paired with a `sha256`; older versions stay reachable via git.
- **The directory pages are rendered by goblog itself**, by a compiled-in
  `directory` plugin that owns the `/plugins` page (dogfooding `Pages()` /
  `RenderPage()`), fetching the index built by the registry repo. goblog.live
  re-serves `index.json`, so it is the canonical URL.
- **Curated registry.** `goblogplatform/plugins` holds a one-line-per-repo list;
  submission is a PR; CI validates on PR and publishes the index to GitHub Pages
  on merge and on a schedule.

## Architecture

```
plugin author                    goblogplatform/plugins (registry)         goblog.live
─────────────                    ────────────────────────────────          ───────────
github.com/you/goblog-plugin-x   registry.yaml: [{repo: you/goblog-plugin-x}]
  goblog-plugin.json    ──PR──►  CI on PR:   validate entry
  plugin.go                      CI on main + every 6h + dispatch: build
  README.md, CHANGELOG.md          → GitHub Pages: index.json,          ──fetch──► directory plugin
  releases vX.Y.Z (+notes)           plugins/<name>.json                           /plugins
                                                                                   /plugins/<name>
                                                                                   /plugins/index.json
```

## Work split

| Piece | Repo | Deliverable |
|---|---|---|
| A | goblog | `goblog validate-plugin <file>` subcommand |
| B | goblog-plugin-hello, plugins | seed plugin repo; registry repo with `registry` tool, CI, Pages |
| C | goblog | plugin-page sub-paths + raw responses; `plugins/directory` plugin; docs |

A and C are independent of B at development time (C is built against a fixture
index). Finishing = B live on Pages, C merged and released, `directory` enabled on
goblog.live via admin settings (no iac change), #552 closed with the index URL noted
for #553.

## Plugin repo contract (documented in `plugins/docs/CONTRACT.md`)

Required at the repo root:

- `goblog-plugin.json`:
  ```json
  {
    "name": "hello",                     // ^[a-z0-9-]+$, unique in the registry, == Name()
    "display_name": "Hello",
    "description": "One sentence shown in the listing.",
    "author": "Jason Ernst",
    "license": "GPL-3.0",                // SPDX identifier
    "entry": "plugin.go",                // the dynamic plugin file; default plugin.go
    "min_goblog_version": "0.2.6",       // semver, no leading v
    "homepage": "https://..."            // optional; defaults to the repo URL
  }
  ```
- The entry `.go` file: `package main`, `func NewPlugin() plugin.Plugin`, loadable
  by goblog's Yaegi loader.
- `README.md` (shown on the detail page).
- Releases tagged `vX.Y.Z`; the tag without `v` must equal the code's `Version()`.
  Release notes are the GitHub release body. Draft and pre-releases are ignored.

Optional: `CHANGELOG.md` (shown on the detail page when present).

## Index format

`index.json` — array, one entry per plugin, sorted by name:

```json
[{
  "name": "hello",
  "display_name": "Hello",
  "description": "…",
  "version": "1.0.0",
  "author": "Jason Ernst",
  "license": "GPL-3.0",
  "source_url": "https://github.com/goblogplatform/goblog-plugin-hello",
  "download_url": "https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go",
  "sha256": "…",
  "min_goblog_version": "0.2.6",
  "install_type": "dynamic",
  "released_at": "2026-09-14T00:00:00Z",
  "detail_url": "https://goblogplatform.github.io/plugins/plugins/hello.json"
}]
```

`install_type` is always `"dynamic"` for now; reserved so compiled-in plugins can be
listed later without changing consumers.

`plugins/<name>.json` — the same fields plus:

```json
{
  "readme_html": "…",
  "changelog_html": "…",              // "" when the repo has no CHANGELOG.md
  "releases": [
    {"version": "1.0.0", "released_at": "…", "notes_html": "…", "url": "https://github.com/…/releases/tag/v1.0.0"}
  ]
}
```

HTML fields are rendered at build time through GitHub's `POST /markdown` (GFM mode,
`context` set to the repo so relative links and `#123` references resolve). GitHub
sanitizes that output, so goblog needs no markdown or sanitizer dependency and
inserts it as `template.HTML`.

## Piece A — `goblog validate-plugin <file>`

- Dispatched at the very top of `main()`, before the `.env` bootstrap (which
  otherwise creates a `.env` in the working directory) and before any DB work.
- Loads the file through the existing Yaegi loader into a throwaway `Registry`
  with no DB. On success prints `{"name":…,"display_name":…,"version":…}` to
  stdout and exits 0; on failure prints the loader error to stderr and exits 1.
- Implemented as a function in `plugin/` (e.g. `plugin.Validate(path) (Info, error)`)
  that `main` wraps, so it is unit-tested against `plugins/dynamic/hello.go.example`
  (success) and a file that fails to compile (error).
- Docker: the image's `ENTRYPOINT` is shell-form (`cd … && ./goblog`), so callers
  use `docker run --rm -v "$PWD:/p" --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:<tag> validate-plugin /p/plugin.go`.
  Documented in the README's dynamic-plugins section.

## Piece C — goblog

### Plugin-page sub-paths and raw responses (plugin system)

- `HookContext` gains `SubPath string`: the part of the request path after the
  page slug, without leading slash (`""` for `/plugins`, `"hello"` for
  `/plugins/hello`, `"index.json"` for `/plugins/index.json`).
- `blog.NoRoute` today only resolves single-segment paths as pages. New rule: if
  the path has more than one segment and the first segment is the slug of a page
  whose `PageType` is owned by a plugin, resolve it to that page and pass the
  remainder as `SubPath`. Non-plugin pages keep the single-segment rule.
- `Registry.RenderPluginPage` treats a page as handled when the plugin returned a
  template name **or** wrote the response itself (`c.Writer.Written()`), so a
  plugin can serve JSON or other raw output. When handled-by-writing, `blog` does
  not render anything further.
- Both documented in the README plugin hook table.

### `plugins/directory`

Compiled-in, registered in `main()`, disabled by default.

Settings:

| key | default | notes |
|---|---|---|
| `enabled` | `false` | standard toggle |
| `index_url` | `https://goblogplatform.github.io/plugins/index.json` | detail URLs come from the index entries |
| `refresh_minutes` | `15` | scheduled job interval; invalid/≤0 falls back to 15 |

- `OnInit` ensures a page row exists: type `plugin-directory`, slug `plugins`,
  title "Plugins", `ShowInNav: true`, `NavOrder: 30` (same pattern as scholar's
  `research` page; the admin can rename/reorder it).
- `fetcher` (own file): holds the last good raw `index.json` bytes, the parsed
  entries, the fetch time, and a per-plugin detail cache keyed by name. `Refresh()`
  fetches the index; details are fetched lazily on first detail-page view and
  re-fetched by the scheduled refresh for names already cached. Any HTTP/parse
  error keeps the previous copy and logs; the first request when the cache is
  empty triggers a synchronous fetch. HTTP client has a 10s timeout and a
  `User-Agent: goblog-directory/<version>`.
- `RenderPage(ctx, "plugin-directory")`:
  - `SubPath == ""` → listing: name, description, author, version, license,
    source link, install type. Empty cache → "The plugin directory is unavailable
    right now" message inside the page (200, not 404).
  - `SubPath == "index.json"` → writes the cached bytes as `application/json`
    with `Cache-Control: public, max-age=300`; 503 with a JSON error when the
    cache is empty.
  - `SubPath == <name>` → detail: README HTML, latest release notes, release
    history, download link, sha256, min goblog version. Unknown name → the
    existing "Page Not Available"-style 404.
  - Anything else → 404.
- Output is `page_content.html` with `plugin_content`, built from `html/template`
  templates embedded in the package (`embed`), so every string from the index is
  escaped and only the pre-rendered `*_html` fields are `template.HTML`.

### Tests

- `fetcher`: `httptest` server fixtures — happy path; malformed JSON keeps last
  good; HTTP 500 keeps last good; empty cache on first request fetches.
- Rendering: listing, `index.json` (content type, bytes verbatim, 503 when empty),
  detail for known and unknown name.
- Registry/blog: sub-path resolution for plugin pages, single-segment rule
  unchanged for normal pages, raw-response handling.

## Piece B — registry and seed plugin

### `goblogplatform/goblog-plugin-hello`

`plugins/dynamic/hello.go.example` copied to `plugin.go`, `goblog-plugin.json`,
`README.md`, `LICENSE` (goblog's), release `v1.0.0`. The example file in goblog
stays (the README's copy-and-run steps use it) and gets one header line pointing
at the repo as the "publish it" example.

### `goblogplatform/plugins`

```
registry.yaml                     # - repo: goblogplatform/goblog-plugin-hello
cmd/registry/                     # module github.com/goblogplatform/plugins
  main.go                         # subcommands: validate, build
  github.go                       # go-github client wrapper behind an interface
  manifest.go                     # goblog-plugin.json parsing + field checks
  index.go                        # index/detail JSON construction
  *_test.go                       # httptest fixtures for releases/contents/markdown
docs/CONTRACT.md                  # plugin repo contract + how to submit
README.md
.github/workflows/validate.yml    # on PR: registry validate for entries changed in the PR
.github/workflows/publish.yml     # push to main, schedule 0 */6 * * *, workflow_dispatch: build → deploy Pages
```

- `registry validate --repo owner/name` (all entries when no flag): latest
  non-draft, non-prerelease release must exist; fetch `goblog-plugin.json` at
  the tag and check required fields, name pattern and uniqueness, SPDX license
  id, semver `min_goblog_version`; fetch `entry` at the tag and run it through
  `docker run --entrypoint … compscidr/goblog:<pinned> validate-plugin`; fail if
  reported name ≠ manifest name or reported version ≠ tag. The Yaegi step sits
  behind an interface so tests do not need Docker.
- The pinned goblog image tag lives in the workflows; Renovate bumps it (same
  pattern as iac).
- `registry build`: validates every entry, writes `dist/index.json` and
  `dist/plugins/<name>.json`, and a static `dist/index.html` that points at
  goblog.live/plugins. An entry that fails validation is logged and skipped so
  one broken upstream release cannot take the directory down; after the Pages
  deploy the workflow exits non-zero if anything was skipped, so it is visible
  in Actions.
- Uses `GITHUB_TOKEN`; markdown rendering via `POST /markdown`.

## Out of scope

- Installing plugins from the directory (#553).
- Compiled-in plugins in the directory (`install_type` reserved for it).
- Theme directory (#560) — reuses the sub-path extension and the registry shape.
- Version history beyond what the release list on the detail page shows.
