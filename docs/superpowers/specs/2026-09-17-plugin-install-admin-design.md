# Admin plugin install, directory ranking and submissions — design

Issues: goblogplatform/goblog#553 (admin install UI), #569 (Registry.Init stops at
first failing OnInit — folded in). Builds on #552 (directory + registry; spec
`2026-09-14-plugin-directory-design.md`). Related: #555 (gin/gorm symbols —
until it lands, dynamic plugins can only add settings and head/footer HTML).

## Goal

From any goblog's admin UI, browse and search the plugin directory, install a
plugin with one click, see which installed plugins have updates, update and
uninstall them — live, without a restart. On goblog.live/plugins, let people
submit their plugin without knowing git. Give the directory a "top plugins"
ordering.

## Decisions

- **Hot-load.** Install/update/uninstall take effect immediately; the plugin
  system gains per-plugin lifecycle (register/unregister, per-plugin job stop).
  Yaegi cannot unload code, so an updated plugin's old interpreter lingers in
  memory until restart (bounded, documented).
- **Ranking = GitHub stars**, added to the index by the registry build
  (`stars` field). Browse sorts by stars by default; name and newest release
  as alternatives.
- **Submission = form → pre-filled GitHub issue → bot validates and opens the
  registry PR.** goblog.live holds no token; the issue template + a workflow in
  `goblogplatform/plugins` do the work; the maintainer still merges.
- **Admin page + JSON API + vanilla JS**, the pattern the other admin pages
  use.
- **Only dynamic plugins are installable/updatable/uninstallable.** Compiled-in
  plugins show as installed, not updatable.
- **Preconditions are surfaced, not hidden:** `ENABLE_DYNAMIC_PLUGINS=true`
  and a writable, persisted `plugins/dynamic/`. Browse works without them;
  Install explains what is missing.
- **Checksum verification is mandatory** (sha256 from the index); mismatch
  refuses without writing anything.
- A plugin is installed only if it loads in Yaegi and its `Name()`/`Version()`
  match the index entry. The UI states that plugins run as Go code inside
  goblog.

## 1. Plugin system (package `plugin`)

- `Register(p)` — compiled-in, unchanged. New `RegisterDynamic(p Plugin, path string)`
  records the source file; `LoadDynamicPlugins` uses it. `loadPlugin` becomes
  exported `LoadDynamicPlugin(path) (Plugin, error)` plus
  `LoadDynamicPluginBytes(src []byte) (Plugin, error)`.
- Registry entries are `{plugin, path, stop chan struct{}}`. `StartScheduledJobs`
  starts each plugin's jobs against that plugin's `stop`. `Stop()` closes all.
- `InitPlugin(name) error` — seed settings + `OnInit` + start jobs for one
  plugin (after a hot install). `Init()` calls the same per-plugin routine for
  every plugin and **continues past failures**, returning `errors.Join` of
  them (#569); `main` logs the joined error.
- `Unregister(name) error` — closes the plugin's `stop`, removes it (the
  `plugins` slice is replaced, not mutated, under the write lock). Settings
  rows are kept. `DeleteSettings(name)` removes them (uninstall only).
- `Dynamic() []DynamicInfo{Name, DisplayName, Version, Path}`.

## 2. Installer (package `plugin/installer`)

```go
type Installer struct {
    Dir       string             // plugins/dynamic
    Registry  *plugin.Registry
    Directory *directory.Fetcher // own instance; URL from the site setting
    Version   string             // running goblog version ("v0.2.7" or "development")
    Client    *http.Client       // 30s timeout; downloads capped at 1 MB
    Enabled   bool               // ENABLE_DYNAMIC_PLUGINS
}
Install(ctx, name) (Result, error) · Update(ctx, name) (Result, error)
Uninstall(name) error · Status(ctx) (Status, error) · Refresh(ctx) error
```

Install: index lookup (refresh if empty/stale) → `install_type == "dynamic"`
→ `min_goblog_version` ≤ running version (semver; skipped when
`development`) → not already registered → download over HTTPS (size cap) →
sha256 equals index → `LoadDynamicPluginBytes` succeeds and `Name()`,
`Version()` match → write `Dir/<name>.go` atomically (temp + rename) →
`RegisterDynamic` + `InitPlugin`. Any failure after the write removes the
file. Update: same checks against the installed dynamic plugin; keeps the old
file as `<name>.go.prev`, `Unregister`, write, load, register; on failure
restores `.prev` and re-registers the old plugin. Uninstall: `Unregister`,
delete file, `DeleteSettings`.

Typed errors → HTTP: `ErrDynamicDisabled`, `ErrIncompatible`, `ErrChecksum`,
`ErrNotDynamic`, `ErrAlreadyInstalled`, `ErrNotInstalled`, `ErrNotFound`,
`ErrLoad` (all 4xx with a message); anything else 500 + log.

`Status` returns:
- `installed[]`: `{name, display_name, version, enabled, dynamic, update_available, latest_version, path}`
  for every registered plugin (update info only for dynamic ones that are in
  the index).
- `available[]`: index entries not installed, plus `stars`, `compatible`.
- `directory_url`, `dynamic_enabled`, `index_fetched_at`, `index_error`.

Site setting `plugin_directory_url` (Admin → Settings), default
`https://www.goblog.live/plugins/index.json`.

## 3. Admin page

- `GET /admin/plugins` → `admin_plugins.html`; nav entry between Post Types
  and Settings (`themes/default` and `goblog-site-theme`).
- API (admin only): `GET /api/v1/plugins/status`;
  `GET /api/v1/plugins/directory?q=&sort=stars|name|newest` (search over
  name, display_name, description, author; case-insensitive);
  `POST /api/v1/plugins/install {name}`; `POST /api/v1/plugins/update {name}`;
  `DELETE /api/v1/plugins/:name`; `POST /api/v1/plugins/refresh`.
- Tabs: **Installed** (display name + name/version, type built-in/dynamic,
  Enabled toggle via existing `/api/v1/plugin-settings`, `Update to vX` when
  available, `Uninstall` for dynamic, link to its settings) and **Browse**
  (search, sort, cards with display name, description, author, version,
  ★ stars, license, `Requires goblog ≥ x`, source/directory links, `Install`;
  disabled with reason when incompatible or already installed).
- Banners: dynamic disabled (how to enable); directory unavailable (+ Refresh);
  dismissible "plugins run as Go code inside goblog" notice; capability note
  ("directory plugins add settings and head/footer HTML; page/job plugins are
  compiled in") until #555.
- Buttons disable + spinner during requests; success re-fetches status;
  failures render the server message inline. No page reloads.

## 4. Registry: stars and submissions (`goblogplatform/plugins`)

- `Source.RepoInfo(ctx, owner, repo) (stars int, err)`; `Build` writes
  `"stars"` to index entries and detail docs. goblog's `directory.Entry` gets
  `Stars`; the listing shows ★ and sorts by stars desc, name asc.
- goblog.live/plugins listing gets a "Submit your plugin" box: a repo URL
  input that opens `https://github.com/goblogplatform/plugins/issues/new?template=submit-plugin.yml&title=Submit:+<owner>/<repo>&repo=<owner>/<repo>`
  in a new tab (client-side only; input validated as `github.com/<owner>/<repo>`),
  with the contract's three requirements and a link to `CONTRACT.md`.
- Registry: `.github/ISSUE_TEMPLATE/submit-plugin.yml` (`repo` input,
  contract checkbox, label `submission`) and `.github/workflows/submit.yml`
  on `issues: [opened, edited]` with the `submission` label:
  - job `check` (`contents: read`): parse `owner/name`, run
    `registry validate --repo` against the current list + the new repo (so
    uniqueness is checked) in the Docker sandbox; output pass/fail + log.
  - job `open-pr` (needs `check` success; `contents: write`,
    `pull-requests: write`, `issues: write`): branch `submit/<owner>-<name>`,
    append the `registry.yaml` line, open PR "Add <owner>/<name>" with
    `Closes #N`, comment the link on the issue. On `check` failure a job
    comments the log on the issue instead.
  - Repo setting: allow Actions to create pull requests.
- The maintainer merges; `Validate` runs on the PR as today.

## 5. Deployment

- iac: goblog.live `.env` gets `ENABLE_DYNAMIC_PLUGINS=true`; bind mount
  `/opt/goblog-live/plugins-dynamic` → `/go/src/github.com/compscidr/goblog/plugins/dynamic`
  (dir created by the role). Optional: same for jasonernst.com prod/staging.
- README documents both prerequisites and that without the mount installs do
  not survive a redeploy.

## Testing

- `plugin`: Unregister stops that plugin's jobs; RegisterDynamic/Dynamic;
  InitPlugin; Init continues past a failing OnInit.
- `plugin/installer`: httptest index + download; happy install; checksum
  mismatch; Name/Version mismatch; incompatible version; already installed;
  dynamic disabled; update success; update rollback; uninstall removes file
  and settings — temp dir + real Registry on sqlite.
- `admin`: router tests for status/directory/install/update/delete incl.
  non-admin 401 and error mapping.
- Registry: RepoInfo in the fake; stars in the index test; submit.yml proven
  with a real issue.
- Manual: fresh goblog with dynamic enabled, install `hello` from the live
  index, footer greeting appears; uninstall.

## PRs

1. goblog — plugin-system lifecycle (+ #569).
2. goblog — installer + API + admin page + README; `stars` in the directory
   listing + Submit box.
3. plugins — `stars`, issue template, `submit.yml`.
4. goblog-site-theme — admin nav + template; iac — env + mount.

## Out of scope

- #555 (gin/gorm symbols) — widens what dynamic plugins can do; the UI copy
  changes when it lands.
- Install counts / telemetry.
- Pinning `download_url` to a commit SHA (sha256 already guards it).
