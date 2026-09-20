# goblog
[![Build and Test](https://github.com/compscidr/goblog/actions/workflows/push.yml/badge.svg)](https://github.com/compscidr/goblog/actions/workflows/push.yml)
[![codecov](https://codecov.io/gh/compscidr/goblog/branch/main/graph/badge.svg)](https://codecov.io/gh/compscidr/goblog)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

A self-hosted blogging platform built with Go. Running at https://www.jasonernst.com

## Features

### Content
- Markdown posts with code syntax highlighting and table support
- Draft / publish workflow
- Post revision history with rollback
- Tags with tag cloud
- Configurable post types (blog posts, notes, etc.)
- Full-text search
- File uploads (images, PDFs, etc.)
- Internal and external backlink tracking
- Comments with markdown support, spam honeypot, and rate limiting; by default commenters must be logged in (GitHub or email code)
- RSS-ready sitemap generation

### Pages
- Configurable dynamic pages (writing, archives, tags, about, custom), plus pages owned by plugins
- Research page listing your publications from Semantic Scholar — install **Scholar Publications** from Admin → Plugins
- Archives sorted by year and month

### Theming
- WordPress-style theme system (`themes/{name}/`)
- Switch themes from admin settings without restart (hot-reload)
- Two built-in themes: `default` (monospace, gray) and `minimal` (sans-serif, blue accent)
- Theme-specific CSS served at `/theme/`
- Custom header/footer code injection via settings (for analytics, etc.)

### Admin
- GitHub OAuth login
- Install wizard for first-time setup
- Admin dashboard with recent comments, and a paginated comments page for moderation
- Configurable settings (site title, subtitle, social URLs, favicon, etc.)
- Post type management
- Page management with hero images/videos

### Plugins
- Plugin system for injecting template data / HTML, scheduled jobs, settings, and whole pages
- Built-in plugins: `analytics`, `socialicons`, `directory` (the plugin directory that runs [goblog.live/plugins](https://goblog.live/plugins); off by default)
- Dynamic plugins: drop a `.go` file in `plugins/dynamic/` — no rebuild (see [Plugins](#plugins))
- WebAssembly plugins (sandboxed, any language/dependencies) installable from the directory
- Install plugins from the [directory](https://www.goblog.live/plugins) with one click under **Admin → Plugins**

### Infrastructure
- SQLite (file-based, zero config), MySQL, or PostgreSQL
- Docker support with tagged releases on Docker Hub
- Configurable trusted proxies for reverse proxy deployments (`TRUSTED_PROXIES` env var)
- GitHub Actions CI/CD

## Quick Start

### Local
```bash
go build
./goblog
```
Visit http://localhost:7000 and follow the install wizard.

### Docker
```bash
docker run -p 7000:7000 compscidr/goblog:latest
```

### Database
SQLite is the default and needs no setup. To use MySQL or PostgreSQL instead, pick it in the install wizard or set the variables in `.env` (see `template.env`):
```bash
database=postgres
POSTGRES_HOST=localhost
POSTGRES_PORT=5432          # default
POSTGRES_USER=goblog
POSTGRES_PASSWORD=...
POSTGRES_DATABASE=goblog
POSTGRES_SSLMODE=disable    # default; or require / verify-ca / verify-full
```
The schema is created and migrated automatically on startup for all three. There is no built-in tool for moving an existing site between databases.

### Behind a Reverse Proxy
Set `TRUSTED_PROXIES` so `X-Forwarded-For` headers are trusted for client IP resolution:
```bash
TRUSTED_PROXIES=172.16.0.0/12 ./goblog
```

### Pinning the Admin Account
On a fresh install the first GitHub account to complete login becomes the admin. If you pre-populate `.env` (e.g. from configuration management) and skip the wizard, anyone could win that race. Pin it to your own account by adding either or both of these to `.env`:
```bash
admin_login=your-github-username      # case-insensitive
admin_github_id=12345                 # numeric id: https://api.github.com/users/your-github-username
```
Other accounts can still log in as regular users but are never promoted. Leave both unset to keep the first-to-login behaviour.

### Managing Admins
The pin above only decides who becomes the *first* admin. After that, admins are managed from the **Users** page in the admin area (`/admin/users`), which lists everyone who has logged in. An existing admin can promote any GitHub user to admin or demote another admin; the last remaining admin can't be demoted, so the site never ends up with none. Email-login users can't be made admin (see #565).

To hand the site over to a different GitHub account: log in with the new account once so it appears in the list, promote it from your current admin account, then log in as the new account and demote the old one.

### Email Login (one-time codes)
Visitors without a GitHub account can log in with an emailed 6-digit code. Add SMTP details to `.env`:
```bash
smtp_host=smtp.example.com
smtp_port=587                         # 465 for implicit TLS; anything else uses STARTTLS when offered
smtp_user=postmaster@example.com      # omit for an unauthenticated relay
smtp_password=...
smtp_from=blog@example.com
```
When `smtp_host` and `smtp_from` are both set the login page offers "sign in with email"; otherwise it shows GitHub only. Codes expire after 10 minutes, allow 5 wrong attempts, and can be re-requested once a minute. Email users are regular users — the admin account is still GitHub-only (see above).

SMTP settings are read once at startup, so restart goblog after changing any `smtp_*` value in `.env` for the change to take effect. Go's SMTP client only sends `smtp_user`/`smtp_password` over an encrypted connection (STARTTLS, or implicit TLS on port 465) unless the host is `localhost`, so if you need an unencrypted remote relay, use it without credentials.

### Comments and Login
Comments require a logged-in user by default: the comment form is replaced by a "Log in to leave a comment" link, and a comment is attributed to the account that posted it (the email is always the account's; the name defaults to the GitHub name or the email's local part but can be edited per comment). Since email login is the way most readers will get an account, configure SMTP as above. If you would rather allow anonymous comments — for example on a site with GitHub login only — untick **comments_require_login** on the admin settings page.

## Theming

Themes live in `themes/{name}/` with this structure:
```
themes/
  default/
    templates/    # HTML templates
    static/       # CSS and assets (served at /theme/)
  minimal/
    templates/
    static/
```

To create a custom theme:
1. Copy `themes/default/` to `themes/my-theme/`
2. Customize templates and CSS
3. Set the `theme` setting to `my-theme` in admin settings

## Plugins

A plugin implements the `plugin.Plugin` interface (`plugin/plugin.go`). Embed `plugin.BasePlugin` to get no-op defaults and implement only the hooks you need:

| Hook | What it does |
|---|---|
| `Name()`, `DisplayName()`, `Version()` | Identity. `Name()` is the unique key used to store the plugin's settings. |
| `Settings()` | Declares settings. They appear under **Admin → Settings** grouped by plugin, are stored in `plugin_settings`, and reach every hook as strings via `ctx.Settings`. The admin UI renders `Type: "textarea"` as a textarea and everything else as a single-line text input (there is no file or checkbox widget for plugin settings yet, so store booleans as `"true"`/`"false"`). Declare an `enabled` setting to get the on/off toggle — the registry calls every plugin regardless, so honour `ctx.Settings["enabled"]` yourself. |
| `TemplateHead(ctx)` / `TemplateFooter(ctx)` | Return raw HTML injected into `<head>` / before `</body>` on every rendered page. Escape anything that came from settings or the request. |
| `TemplateData(ctx)` | Returns data made available to templates as `.plugins.<name>`. |
| `ScheduledJobs()` | Periodic background jobs (`Name`, `Interval`, `Run(db, settings)`), started at boot. |
| `Pages()` / `RenderPage(ctx, pageType)` | Own a page type: it gets a slug, an optional nav entry, and you choose the template and data when it is visited. The plugin also owns everything under its slug: `ctx.SubPath` is `""` for `/research`, `"2024"` for `/research/2024`. Return a template name to render it inside the theme, or write the response yourself (e.g. `ctx.GinContext.JSON(...)`) and return `""`; returning `""` without writing anything gives a 404. `plugins/directory` is the example: it serves `/plugins`, `/plugins/<name>` and `/plugins/index.json`. |
| `OnInit(db)` | Runs once at startup, after settings are seeded. |

`ctx` is a `*plugin.HookContext` carrying the Gin context, the DB, the plugin's own settings, the template being rendered, and the existing template data. `plugins/socialicons` is the smallest complete example.

#### Upgrading to 0.3.0
The `scholar` plugin is no longer compiled in; it is now **Scholar Publications** in the plugin directory ([goblogplatform/goblog-plugin-scholar](https://github.com/goblogplatform/goblog-plugin-scholar)). After upgrading, install it from **Admin → Plugins**. Your Research page and the plugin's settings carry over (same plugin name and page type), so the page reappears in the nav as soon as the plugin is installed **and enabled** — its `enabled` setting defaults to `false` on a fresh install, while a site that already had `scholar.enabled=true` keeps it. Until then the page is hidden and `/research` answers "Page Not Available". The new plugin reads from the Semantic Scholar API only — if you were using Google Scholar, set `semantic_scholar_id` (the number at the end of your semanticscholar.org author URL) under **Admin → Settings → Scholar Publications**. Docker users: bind-mount `plugins/wasm/` first (see [Installing from the directory](#installing-from-the-directory)), or the installed plugin vanishes when the container restarts.

### Compiled-in plugins
Live in `plugins/<name>/` as a normal Go package, and are registered in `main()`:
```go
registry.Register(myplugin.New())
```
They have full access to `gin`, `gorm`, and any module dependency, and are part of the release binary. Use this for anything that ships with goblog.

### WebAssembly plugins
The plugin directory installs sandboxed WebAssembly modules built with [Extism](https://extism.org/): any language with an Extism PDK, any dependencies, no goblog rebuild. A plugin has no filesystem access and can reach the network only at the hosts it declares.

Every export takes and returns JSON through Extism's input/output. Only `identity` is mandatory; a missing export behaves like `BasePlugin`'s no-op.

| Export | Input → Output | Mirrors |
|---|---|---|
| `identity` | → `{name, display_name, version}` | `Name/DisplayName/Version` |
| `settings` | → `[{key, type, default, label, description}]` | `Settings()` |
| `pages` | → `[{page_type, title, slug, show_in_nav, nav_order, description}]` | `Pages()` |
| `jobs` | → `[{name, interval_seconds}]` (`interval_seconds` ≤ 0 → 1 h) | `ScheduledJobs()` |
| `template_head` / `template_footer` | `ctx` → HTML string | same |
| `template_data` | `ctx` → JSON object (`.plugins.<name>`) | `TemplateData` |
| `render_page` | `ctx` → `{"html": "…"}` **or** `{"template": "x.html", "data": {…}}` **or** `{"raw": {"status", "content_type", "body"}}` | `RenderPage` |
| `run_job` | `{"name", "settings"}` → `{}` | `ScheduledJob.Run` |
| `on_init` | `{"settings": {k: v}}` (current values: defaults overlaid with what is stored) → `{}` | `OnInit` |

`ctx` is `{"settings": {k: v}, "template": "…", "request": {"path", "sub_path", "query": {k: v}, "method"}}`.

Host functions (Extism's `extism:host/user` namespace):

| Function | Behaviour |
|---|---|
| `store_get(key) → value \| null` | per-plugin persistent KV (`plugin_store` table) |
| `store_set(key, value)` | key ≤ 256 bytes, value ≤ 1 MiB, at most 10 000 keys and 16 MiB per plugin; errors otherwise |
| `store_delete(key)` | |
| `store_list(prefix) → [keys]` | |
| logging | the Extism PDK's own logger (`pdk.Log`), prefixed with the plugin name — there is no separate `log` host function |
| HTTP | Extism's built-in `http_request`, limited to the hosts in the plugin's `allowed_hosts`; none declared → no network |

Limits: 10 s per `template_*`/`render_page` call, 120 s for `run_job`/`on_init`; 64 MB memory per plugin; one loaded instance per plugin, calls serialised behind a mutex. A call that hits its timeout closes the instance; the next call re-creates it from the module bytes and carries on, at most once per 30 s — a plugin that keeps timing out declines (empty hooks, 404 pages) in between. HTTP redirects are checked against `allowed_hosts` on every hop.

Build one with the standard Go toolchain and [`github.com/extism/go-pdk`](https://github.com/extism/go-pdk):
```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```
[`plugin/wasm/testdata/echo/main.go`](plugin/wasm/testdata/echo/main.go) is the reference implementation of every export, including the store and an outbound HTTP call.

An installed wasm plugin is `plugins/wasm/<name>.wasm` plus a `<name>.json` sidecar declaring its allowed hosts:
```json
{"allowed_hosts": ["api.example.test"]}
```
The installer writes this sidecar for directory installs; an operator dropping a `.wasm` file by hand can ship its own (no sidecar means no network access). Loading is on by default — set `ENABLE_WASM_PLUGINS=false` to turn it off.

`goblog validate-plugin <file.go|file.wasm>` with a `.wasm` file loads the module with no store or network access, calls `identity`/`settings`/`pages`/`jobs`, and prints the identity as JSON:
```bash
./goblog validate-plugin plugin.wasm
# {"name":"echo","display_name":"Echo","version":"1.2.3","runtime":"wasm"}
```

#### Installing from the directory
**Admin → Plugins** lists what is installed and lets you browse and search the [plugin directory](https://www.goblog.live/plugins), install a plugin with one click, update it when the directory has a newer release, or uninstall it. WebAssembly is the only format the directory installs; requirements:
- `plugins/wasm/` writable by goblog (WASM loading is on by default; set `ENABLE_WASM_PLUGINS=false` to disable it entirely). With Docker, bind-mount that directory (see below) — otherwise installed plugins vanish with the container.
- The directory URL is the `plugin_directory_url` setting (default `https://www.goblog.live/plugins/index.json`); point it elsewhere to run a private directory. `plugin_directory_url` is a trust decision: whatever it points at can offer code that runs inside goblog once you click Install.

Install downloads the plugin's `.wasm` asset, verifies its sha256 against the directory index, loads it, checks that its name and version match, and only then writes it (plus the `allowed_hosts` sidecar) to `plugins/wasm/` and starts it — no restart. Updates keep the plugin's settings; uninstall removes both files. Install only from sources you trust. A dynamic (`.go`) plugin installed before the directory went wasm-only can still be updated to a wasm release or uninstalled, just not reinstalled as `.go`.

### Dynamic plugins
Yaegi plugins are the local/operator path for extending goblog without a rebuild — not a directory-installable format (see WebAssembly plugins above for that). Loaded at startup (or, for one previously installed from the directory, kept running) from `plugins/dynamic/*.go` by the embedded [Yaegi](https://github.com/traefik/yaegi) Go interpreter. Enable with:
```bash
ENABLE_DYNAMIC_PLUGINS=true ./goblog
```
A dynamic plugin is a single `package main` file defining `func NewPlugin() plugin.Plugin`. Start from the shipped example:
```bash
cp plugins/dynamic/hello.go.example plugins/dynamic/hello.go
ENABLE_DYNAMIC_PLUGINS=true ./goblog     # every page now ends with a greeting
```
then edit the message under **Admin → Settings → Hello (example)**.

Limits of the interpreted environment:
- Available imports are the Go standard library and `goblog/plugin` (`Plugin`, `BasePlugin`, `HookContext`, `SettingDefinition`, `ScheduledJob`, `PageDefinition`). `gin` and `gorm` are **not** available, so the hooks that name their types — `TemplateData`, `ScheduledJobs`, `OnInit`, `RenderPage` — can't be implemented dynamically; write a compiled-in plugin for those.
- A file that fails to load is logged and skipped; the rest still load.
- Dynamic plugins run as ordinary Go code inside the goblog process with stdlib access, unsandboxed. Only load files you control; `plugins/dynamic/` should be writable by the operator alone.

With Docker, bind-mount the directory and set the flag:
```bash
docker run -p 7000:7000 -e ENABLE_DYNAMIC_PLUGINS=true \
  -v $PWD/plugins/dynamic:/go/src/github.com/compscidr/goblog/plugins/dynamic \
  -v $PWD/plugins/wasm:/go/src/github.com/compscidr/goblog/plugins/wasm \
  compscidr/goblog:latest
```

#### Checking a plugin file
`goblog validate-plugin <file>` accepts either a Yaegi `.go` file or a wasm `.wasm` module, loads it in isolation, and prints its identity as JSON (exit 1 with the load error on stderr if it fails):
```bash
./goblog validate-plugin plugins/dynamic/hello.go.example
# {"name":"hello","display_name":"Hello (example)","version":"1.0.0"}
./goblog validate-plugin plugin.wasm
# {"name":"echo","display_name":"Echo","version":"1.2.3","runtime":"wasm"}
```
With the Docker image (its entrypoint is a shell command, so override it):
```bash
docker run --rm --network none -v "$PWD:/p" --entrypoint /go/src/github.com/compscidr/goblog/goblog \
  compscidr/goblog:latest validate-plugin /p/plugin.wasm
```
This is what the [plugin directory](https://goblog.live/plugins) registry runs on every submission.

### Plugin directory
[goblog.live/plugins](https://goblog.live/plugins) lists published plugins; `https://goblog.live/plugins/index.json` is the same list as JSON (name, version, author, license, `download_url`, `sha256`, `min_goblog_version`, `runtime`, `allowed_hosts`). Plugins are individual GitHub repositories with releases; the curated list and the build that produces the index live in [goblogplatform/plugins](https://github.com/goblogplatform/plugins), which also documents how to submit one.

The pages are rendered by the built-in `directory` plugin, which any goblog can turn on under **Admin → Settings → Plugin Directory** (`enabled` = `true`). It fetches `index_url` every `refresh_minutes`, keeps the last good copy if the registry is unreachable, and serves `/plugins`, `/plugins/<name>` and `/plugins/index.json`. Only point `index_url` at a registry you trust: its README, changelog and release-note HTML is shown as-is. The directory page also has a **Submit your plugin** box: paste your repository URL and it opens a pre-filled submission on GitHub.

## Testing
```bash
go test ./...
```

## Architecture

- **Gin** for HTTP routing and middleware
- **GORM** for database ORM (SQLite, MySQL support)
- **Showdown.js** + **DOMPurify** for client-side markdown rendering
- **Bootstrap 5** for UI framework
- Server-side rendered templates with JSON REST API at `/api/v1/`
