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
- Comments with markdown support, spam honeypot, and rate limiting
- RSS-ready sitemap generation

### Pages
- Configurable dynamic pages (writing, research, archives, tags, about, custom)
- Google Scholar integration for research pages (with caching and throttle resilience)
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
- Built-in plugins: `analytics`, `socialicons`, `scholar` (research page)
- Dynamic plugins: drop a `.go` file in `plugins/dynamic/` — no rebuild (see [Plugins](#plugins))

### Infrastructure
- SQLite database (file-based, zero config)
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
| `Pages()` / `RenderPage(ctx, pageType)` | Own a page type: it gets a slug, an optional nav entry, and you choose the template and data when it is visited. `plugins/scholar` is the example. |
| `OnInit(db)` | Runs once at startup, after settings are seeded. |

`ctx` is a `*plugin.HookContext` carrying the Gin context, the DB, the plugin's own settings, the template being rendered, and the existing template data. `plugins/socialicons` is the smallest complete example.

### Compiled-in plugins
Live in `plugins/<name>/` as a normal Go package, and are registered in `main()`:
```go
registry.Register(myplugin.New())
```
They have full access to `gin`, `gorm`, and any module dependency, and are part of the release binary. Use this for anything that ships with goblog.

### Dynamic plugins
Loaded at startup from `plugins/dynamic/*.go` by the embedded [Yaegi](https://github.com/traefik/yaegi) Go interpreter — no rebuild, so they work with the Docker image. Enable with:
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
- Dynamic plugins run as ordinary Go code inside the goblog process with stdlib access. Only load files you control; `plugins/dynamic/` should be writable by the operator alone.

With Docker, bind-mount the directory and set the flag:
```bash
docker run -p 7000:7000 -e ENABLE_DYNAMIC_PLUGINS=true \
  -v $PWD/plugins/dynamic:/go/src/github.com/compscidr/goblog/plugins/dynamic \
  compscidr/goblog:latest
```

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
