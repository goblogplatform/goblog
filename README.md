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
- Admin dashboard with recent comments
- Configurable settings (site title, subtitle, social URLs, favicon, etc.)
- Post type management
- Page management with hero images/videos

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

## Todo
- PostgreSQL support (#14)
- Plugin system (#480)
- Cross-posting to other platforms (#12)
- Research page citation counts (#513)
