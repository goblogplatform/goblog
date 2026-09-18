# WebAssembly plugins — design

Related: #555 (superseded by this design), #552 (directory), #553 (admin install),
scholar plugin (`plugins/scholar`, to be replaced).

## Goal

Let plugin authors ship self-contained plugins with any dependencies they like,
without goblog having to know about those dependencies, and let operators install
them from the directory safely. Convert the scholar publications plugin into the
first such plugin.

## Decisions

- **Runtime: WebAssembly via Extism** (`github.com/extism/go-sdk`, wazero
  underneath; pure Go, no cgo). A plugin is one `.wasm` artifact built by its
  author — with the standard Go toolchain
  (`GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`, exports via
  `//go:wasmexport`) or TinyGo, or any language with an Extism PDK.
- **Dependencies are the author's business.** goblog owns only the host
  contract below. Adding a library to a plugin never requires a goblog change.
- **Sandboxed by construction**: no filesystem, no network except hosts the
  plugin's manifest declares, per-call timeouts, memory cap, one instance per
  plugin behind a mutex.
- **WASM is the only directory-installable format.** Yaegi `.go` plugins keep
  working for operator-dropped files (`ENABLE_DYNAMIC_PLUGINS=true`,
  `plugins/dynamic/`) but are not a submission format.
- **WASM loading is on by default** (`ENABLE_WASM_PLUGINS`, default `true`).
- **Scholar** becomes `goblogplatform/goblog-plugin-scholar`; the compiled-in
  plugin is removed in goblog **v0.3.0**. Same `Name()` (`scholar`) and page
  type (`research`), so settings and the Research page carry over.

## 1. Host contract

Every export takes and returns JSON through Extism's input/output. Only
`identity` is mandatory; missing exports act like `BasePlugin` no-ops.

| Export | Input → Output | Mirrors |
|---|---|---|
| `identity` | → `{name, display_name, version}` | `Name/DisplayName/Version` |
| `settings` | → `[{key, type, default, label, description}]` | `Settings()` |
| `pages` | → `[{page_type, title, slug, show_in_nav, nav_order, description}]` | `Pages()` |
| `jobs` | → `[{name, interval_seconds}]` | `ScheduledJobs()` |
| `template_head` / `template_footer` | `ctx` → HTML string | same |
| `template_data` | `ctx` → JSON object (`.plugins.<name>`) | `TemplateData` |
| `render_page` | `ctx` → `{"html": "…"}` **or** `{"template": "x.html", "data": {…}}` **or** `{"raw": {"status", "content_type", "body"}}` | `RenderPage` |
| `run_job` | `{name}` → `{}` | `ScheduledJob.Run` |
| `on_init` | → `{}` | `OnInit` |

`ctx` = `{"settings": {k: v}, "template": "…", "request": {"path", "sub_path", "query": {k: v}, "method"}}`.

Host functions (namespace `goblog`):

| Function | Behaviour |
|---|---|
| `store_get(key) → value \| null` | per-plugin persistent KV (`plugin_store` table: plugin_name, key, value, updated_at) |
| `store_set(key, value)` | key ≤ 256 B, value ≤ 1 MB; errors otherwise |
| `store_delete(key)` | |
| `store_list(prefix) → [keys]` | |
| `log(level, msg)` | goblog log, prefixed with the plugin name |
| HTTP | Extism's built-in `http_request`, limited to the manifest's `allowed_hosts`; none → no network |

Limits: 10 s per template/render call, 120 s for `run_job`/`on_init`; 64 MB memory
per plugin; one instance per plugin, mutex-serialised. goblog creates page rows
from `pages` and filters them by the `enabled` setting exactly as for
compiled-in plugins; plugins never touch the DB.

## 2. goblog runtime (sub-project A)

- Package `plugin/wasm`: `Load(path string, opts Options) (*Plugin, error)`;
  `*Plugin` implements `plugin.Plugin`. `Options{Store, Logger, AllowedHosts}`.
  `plugin.Store` interface (`Get/Set/Delete/List`) with a gorm implementation
  in `plugin` (`plugin_store`, migrated by `Registry.Init`).
- `allowed_hosts` come from a sidecar `plugins/wasm/<name>.json`
  (`{"allowed_hosts": [...]}`) written by the installer from the directory
  entry; operator-dropped files may ship their own; no sidecar → no network.
- Loader: `LoadWasmPlugins(registry, "plugins/wasm")` at boot when
  `ENABLE_WASM_PLUGINS != "false"`. `Registry.RegisterDynamic` gains a runtime
  label; `DynamicInfo.Runtime` is `"wasm"` or `"go"`.
- Adapter: `identity`, `settings`, `pages`, `jobs` are called once at load and
  cached; hooks per call. `render_page` results map onto the existing template /
  raw-response paths. Export errors are logged and surface as the same
  "plugin unavailable" notice scholar shows today.
- `goblog validate-plugin` accepts `.wasm`: loads with no host access, calls
  `identity`, `settings`, `pages`, `jobs`, prints identity + `"runtime":"wasm"`.
- Installer: `install_type: "wasm"` → `plugins/wasm/<name>.wasm` + sidecar;
  loaded via `wasm.Load`; other types are "not installable". Update/uninstall
  keep their shape (sidecar written atomically before the load; removed on
  uninstall).
- Admin page: Browse cards show "Talks to: <hosts>" or "no network"; Installed
  rows label `wasm` / `go` / `built-in`; capability note updated.
- Tests: a fixture `.wasm` built from `plugin/wasm/testdata/echo` with the
  standard toolchain and committed (with a `go:generate` line); adapter tests
  for every export incl. missing ones, store functions, timeout, memory cap,
  allowed-hosts denial; loader/installer/validate tests reuse it.

## 3. Registry contract v2 (sub-project B)

- `goblog-plugin.json`: `runtime: "wasm"` (required), `entry: "plugin.wasm"`
  (the release **asset** name), `allowed_hosts: [...]` (optional). Non-wasm
  entries are rejected with a pointer to CONTRACT.md.
- Releases carry the `.wasm` as an asset; CONTRACT ships a copy-paste GitHub
  workflow that builds and uploads it on tag. `download_url` is the asset's
  browser URL; `sha256` is of the asset (≤ 16 MB).
- `Source.ReleaseAsset(ctx, owner, repo, tag, name) ([]byte, error)`;
  `ValidateEntry` validates the asset in the Docker sandbox via
  `validate-plugin plugin.wasm`.
- Index/detail gain `runtime`, `allowed_hosts`; `install_type: "wasm"`.
- goblog `directory.Entry` gains `Runtime`, `AllowedHosts`; listing shows them;
  the installer marks non-wasm entries incompatible.
- `goblogplatform/goblog-plugin-hello` v2.0.0 rewritten as WASM (reference
  example); goblog's `hello.go.example` stays the Yaegi example.

## 4. Scholar WASM plugin (sub-project C)

`goblogplatform/goblog-plugin-scholar`, standard Go, identity
`scholar` / "Scholar Publications" / `2.0.0`, page type `research`, slug
`research`. Settings: `enabled`, `semantic_scholar_id`,
`semantic_scholar_api_key`, `article_limit` (50), `cache_hours` (24). Google
Scholar is dropped. `render_page` serves from `store_get("articles")`,
fetching from `api.semanticscholar.org` (`/graph/v1/author/{id}/papers`) when
missing/stale and rendering the same HTML as today with `html/template`; a
`refresh` job every `cache_hours` keeps the cache warm; fetch failure with no
cache → the "temporarily unavailable" notice. `allowed_hosts:
["api.semanticscholar.org"]`. Unit tests run natively (fetch behind an
interface).

## 5. goblog v0.3.0 and deploy (sub-project D)

Remove `plugins/scholar` and the `compscidr/scholar` dependency; README and
release notes: install **Scholar Publications** from Admin → Plugins, settings
and the Research page carry over. iac: `plugins/wasm` bind mount for
jasonernst.com prod/staging and goblog.live; install scholar after deploy.

Order: A → B → C → D.

## Out of scope

- Exposing gin/gorm or third-party libraries to Yaegi (#555 closes as superseded).
- Multi-instance / parallel calls into one plugin.
- Plugin-to-plugin calls, plugin-provided admin pages.
