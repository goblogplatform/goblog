# Theme directory — design

Issue: goblogplatform/goblog#560. Builds on the database-backed plugin
directory (`2026-09-20-db-backed-plugin-directory-design.md`): same
curation flow, same tables, same admin surface, generalised to two kinds.

## Goal

goblog.live/themes showcases themes (screenshot, description, author, stars)
submitted by anyone and approved by an admin, and any goblog installs one
from **Admin → Themes** with one click — download at a tag, validate, unpack,
activate — the way plugins work today.

## Decisions

- **Showcase and one-click install**, mirroring plugins end to end.
- **A theme release is a `vX.Y.Z` tag; the artifact is GitHub's tag zipball.**
  No build step or release workflow for theme authors. Because GitHub does
  not guarantee archive byte-stability, the index's `sha256` is a **content
  hash** of the extracted `templates/` and `static/` trees, not of the zip.
- **Screenshots are hot-linked** from `raw.githubusercontent.com` at the
  release tag; goblog.live stores nothing.
- **Theme templates override `themes/default` by name.** The loader parses
  `templates/shared` → `themes/default/templates` → the active theme, so a
  theme only ships what it customises, `minimal`'s missing admin pages stop
  500ing, and validation does not chase goblog's template list every release.
  `/theme/*` static files fall back to `themes/default/static` the same way.
- **Installed themes live in `themes/installed/<name>/`** (bind-mounted in
  Docker); built-ins stay in the image. One resolver looks in `themes/<name>`
  then `themes/installed/<name>`. `installed` is a reserved theme name.
- **The `directory` plugin gains a `kind`** (`plugin` | `theme`) rather than a
  second plugin: same `Service`, same admin tab, separate public pages and
  a separate `/themes/index.json` so older goblogs' installers never see
  themes in `/plugins/index.json`.

## 1. Theme repository contract (`docs/THEME_CONTRACT.md`)

Required at the repo root, at the release tag:

- `goblog-theme.json`:
  ```json
  {
    "name": "forest",                 // ^[a-z0-9-]+$; directory name and the `theme` setting value
    "display_name": "Forest",
    "description": "One sentence shown in the listing.",
    "author": "Your Name",
    "license": "MIT",                 // SPDX id from the plugin contract's list
    "min_goblog_version": "0.5.0",    // plain semver; the first goblog with the override loader
    "homepage": "https://..."         // optional
  }
  ```
  `name` may not be `default`, `minimal`, `installed`, `shared` (`forest`
  ships compiled in and is also the first directory theme; the installer
  never installs over a built-in).
- `templates/` with at least one `*.html`; each must parse with goblog's
  template functions on top of `templates/shared` and `themes/default`.
- `static/` (optional).
- `README.md` (shown on the detail page).
- `screenshot.png` or `screenshot.jpg`, ≤ 1 MiB (GitHub's contents API
  returns files up to 1 MiB inline).
- Releases tagged `vX.Y.Z`; drafts and pre-releases ignored; the release body
  is the version's notes.

Limits on the archive: ≤ 16 MiB compressed, ≤ 2000 entries, no symlinks, no
entries outside `templates/` and `static/` are installed (others are
ignored), no `..` or absolute paths.

## 2. Registry (`plugins/directory/registry`)

- `Source` gains `Zipball(ctx, owner, repo, tag) ([]byte, error)` —
  `GET /repos/{o}/{r}/zipball/{tag}` (follows the redirect; `MaxAssetBytes`
  cap) — and `FileURL(owner, repo, tag, path) string` for the raw screenshot
  URL.
- `theme.go`: `ThemeManifest` + `ParseThemeManifest`; `ParseArchive(zip
  []byte) (map[string][]byte, error)` (the zip's files restricted to
  `templates/` and `static/`, with the entry and size checks above; the
  single top-level folder GitHub adds is stripped); `ContentHash(files) string` — sha256 over
  `path\0len\0bytes` for every file, sorted by path; `ValidateThemeEntry(ctx,
  src, tv ThemeValidator, repo)` and `BuildTheme(ctx, src, tv, repo,
  baseURL) (DetailDoc, error)`, sharing `latestRelease`, README/changelog
  rendering and stars with the plugin path.
- `ThemeValidator` interface: `Validate(ctx, files map[string][]byte) error`.
  `TemplateValidator` (in package `theme`, see §4) parses the theme's
  templates on top of shared + default with goblog's func map; `registry`
  takes it as an interface so it has no dependency on the theme loader.
- `IndexEntry` gains `Kind string json:"kind"` and `ScreenshotURL string
  json:"screenshot_url,omitempty"`; `InstallType` is `"theme"` and `Runtime`
  `""` for themes. `AllowedHosts` is `[]`.

## 3. Directory plugin (`plugins/directory`)

- `Repo.Kind` and `Build.Kind` (`string`, indexed, default `plugin` for
  existing rows via a migration step that backfills empty kinds). Uniqueness
  of `Build.Name` becomes a composite index on `(kind, name)`; `Repo.Repo`
  stays unique (a repo is either a plugin or a theme).
- `Service`: `Submit(ctx, kind, input, ip, token)`, `Add(ctx, kind, input,
  token)`, `Index(kind)`, `Detail(kind, name)`, `nameOf(kind, repo)`; the
  cache holds one `raw`/`entries` per kind; `RefreshAll` rebuilds by the
  row's kind; `List`/`Get` return `kind`. `ParseRepo` unchanged.
- Pages: a second `PageDefinition` `theme-directory`, slug `themes`, title
  "Themes", nav order 31 (`OnInit` ensures both rows). `RenderPage` switches
  on page type to pick the kind; sub-paths are the same shape
  (`""`, `index.json`, `submit`, `<name>`, `<name>.json`).
- Templates: `themes-listing.html` (card grid: screenshot, display name,
  version, description, author, license, ★), `themes-detail.html` (large
  screenshot, metadata, README, releases), `submit.html` parameterised by
  kind (contract text and link differ).
- Admin: `RepoView.Kind`; the Directory tab shows a kind badge, pending
  theme cards show the screenshot; `POST /api/v1/directory/repos {repo,
  kind}` where `kind` is optional. **Kind is detected, not chosen:** a
  submission (public form or admin Add) with no kind looks at which of
  `goblog-plugin.json` / `goblog-theme.json` the latest release carries
  (`registry.DetectKind`; both or neither is a validation error), so either
  submit page accepts either kind and the confirmation says which it became.

## 4. Theme loader (`theme` package, new)

Extracted from `goblog.go`:

- `theme.Dir(name) (string, bool)` — `themes/<name>` if it has `templates/`,
  else `THEMES_INSTALLED_DIR/<name>` (default `themes/installed`).
- `theme.List() []string` — built-ins plus installed, deduplicated,
  `installed` skipped.
- `theme.Load(name, funcMap) (*template.Template, string)` — shared → default
  → theme; on a parse error logs and returns default. Used by the startup
  path and `OnThemeChange`.
- `theme.StaticHandler(active func() string)` — serves `/theme/*` from the
  active theme, falling back to `themes/default/static`.
- `theme.ValidateFiles(files map[string][]byte) error` — the
  `TemplateValidator` for the registry and the installer: parses shared +
  default from disk, then every `templates/*.html` in `files`, reporting
  the first parse error with its file name.

## 5. Installer (`theme/installer`, new) and Admin → Themes

```go
type Installer struct {
    Dir          string              // themes/installed
    Directory    *installer.Fetcher  // themes index (its own instance)
    Version      string
    Client       *http.Client
    IndexURL     func() string       // site setting theme_directory_url
    ActiveTheme  func() string
    Activate     func(name string) error // writes the setting + OnThemeChange
}
Status(ctx) Status · Install(ctx, name) (Result, error) · Update(ctx, name) (Result, error)
Uninstall(name) error · Activate(name) error · Refresh(ctx) error
```

- `Status`: `installed[]` (`name, display_name, version, builtin, active,
  update_available, latest_version`), `available[]` (index entries not
  installed, with `compatible`, `screenshot_url`), `active`, `dir_writable`,
  `index_fetched_at`, `index_error`.
- Install: index lookup → `install_type == "theme"` → `min_goblog_version`
  ≤ running → not built-in, not installed → download `download_url` (16 MiB
  cap) → `registry.ParseArchive` → `ContentHash == sha256` → `theme.ValidateFiles`
  → write to `Dir/<name>.tmp/` → rename to `Dir/<name>/` → write
  `goblog-theme.json` copy with the version for `Status`. Update: same into
  `.tmp`, previous dir kept as `<name>.prev` until the new one is in place,
  then removed; on failure `.prev` is restored. Uninstall refuses the active
  theme and built-ins. Activate refuses unknown names.
- Typed errors → HTTP like the plugin installer: `ErrNotFound`,
  `ErrIncompatible`, `ErrChecksum`, `ErrAlreadyInstalled`, `ErrNotInstalled`,
  `ErrBuiltin`, `ErrActive`, `ErrLoad`, `ErrDirectoryUnavailable`,
  `ErrDownload`, `ErrWrite`.
- Site setting `theme_directory_url`, default
  `https://www.goblog.live/themes/index.json`.
- Routes (admin only): `GET /admin/themes`; `GET /api/v1/themes/status`;
  `GET /api/v1/themes/directory?q=&sort=`; `POST /api/v1/themes/install
  {name}`; `POST /api/v1/themes/update {name}`; `POST /api/v1/themes/activate
  {name}`; `DELETE /api/v1/themes/:name`; `POST /api/v1/themes/refresh`.
- `admin_themes.html` (`themes/default`, mirrored to the site theme):
  **Installed** table (name, version, built-in/installed badge, Active
  marker, Activate / Update / Uninstall) and **Browse** (screenshot cards,
  Install; disabled with reason when incompatible or the dir is not
  writable). Nav entry after Plugins. Settings' `theme` dropdown keeps
  working (it lists `theme.List()`).

## 6. Rollout

- goblog release `v0.5.0` (loader change + installer + directory kind).
- iac: bind-mount `/opt/goblog-live/themes-installed` →
  `…/goblog/themes/installed` (and the same for jasonernst.com prod/staging).
- Seed: move `forest` into `goblogplatform/goblog-theme-forest` following the
  contract (it also stays compiled in for now); Add it in the Directory tab.
  goblog.live's own `site` theme is not listed (site-specific).
- README: "Theming" section documents the override loader, `themes/installed`,
  Admin → Themes and the contract link; `docs/THEME_CONTRACT.md`.
- Seed forest promptly after deploying PR 2: `OnInit` creates the public
  Themes page (in the nav) immediately, so it is empty until the first theme
  is approved.

## 7. Testing

- `registry`: zip fixtures built in-test — valid theme; top-level folder
  stripped; traversal (`../`), absolute path, symlink, > 2000 entries,
  oversize; files outside `templates/`/`static/` ignored; `ContentHash`
  deterministic and order-independent; manifest rules incl. reserved names;
  screenshot missing / too large; `ValidateThemeEntry` error table;
  `BuildTheme` entry fields incl. `screenshot_url` and `kind`.
- `theme`: `Load` override-by-name (a theme with only `home.html` renders
  every other page from default); parse error falls back to default;
  `Dir`/`List` resolution across both roots; static fallback; `ValidateFiles`
  first-error reporting.
- `plugins/directory`: kind separation (a theme and a plugin may share a
  name; each index lists only its kind; `/plugins/<name>` never serves a
  theme); submit/add with kind; migration backfills `plugin`; both pages
  render; admin list/add carry `kind`.
- `theme/installer`: httptest index + zipball; happy install; hash mismatch
  refused without writing; incompatible; already installed; built-in
  refused; update success and rollback; uninstall of active refused;
  activate sets the setting.
- `admin`: router tests for the six theme endpoints, non-admin 401, nil
  installer 503; `csrf_test.go` rows for `/api/v1/themes/activate`.
- Manual: install forest from the live index on a fresh goblog, activate,
  see it render, uninstall (refused while active), switch back, uninstall.

## PRs

1. goblog — `theme` package (loader extraction, override, resolver, static
   fallback, `ValidateFiles`) + README.
2. goblog — registry theme validator/builder + directory `kind` + `/themes`
   pages + admin Directory tab changes + `docs/THEME_CONTRACT.md`.
3. goblog — `theme/installer` + Admin → Themes page + API + setting.
4. goblog-theme-forest — new repo from `themes/forest`.
5. goblog-site-theme — `admin_themes.html` + nav.
6. iac — mounts + version bump.

## Out of scope / accepted risks

- Theme previews on goblog.live (rendering a theme with sample content).
- Per-theme settings or a customiser.
- Themes that need DB migrations or plugin-specific templates beyond what
  the override loader provides.
- Screenshot hot-linking means visitors' browsers request GitHub; acceptable
  for a showcase.
- The same public-submission fences as plugins (rate limit, validation lock,
  budget); a theme zip is parsed in memory with the caps above, never
  executed.
