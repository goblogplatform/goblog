# Writing a theme

A theme is a directory with `templates/` and, optionally, `static/`. goblog loads its templates **on top of `themes/default`**, so you ship only the files you change: a template you leave out renders from default, and so does any static file you do not provide. The forest theme, [goblog-theme-forest](https://github.com/goblogplatform/goblog-theme-forest), overrides sixteen public templates and one stylesheet and leaves every admin and wizard page to default.

This page is about the templates: how they are loaded, what data they get, how to try one locally. What a theme repository must look like to be listed in the directory — the manifest, the screenshot, the archive limits — is in [Publishing a theme](/docs/publishing-a-theme).

## How loading works

At startup, and again whenever the `theme` setting changes, goblog builds one Go [`html/template`](https://pkg.go.dev/html/template) set in three layers, each parsed on top of the last:

1. **Shared:** `templates/shared/*.html`. Today that is `_head.html`, which defines the `_head` template — the `<head>` every page starts with (meta tags, the CDN stylesheets, `/theme/css/goblog.css`, the `custom_header_code` setting and the plugins' head HTML).
2. **Default:** `themes/default/templates/*.html`.
3. **Yours:** `<theme>/templates/*.html`.

Templates are named by file name, so your `header.html` replaces default's `header.html` and nothing else. Only files directly under `templates/` are parsed; a subdirectory is installed but never loaded. A template that fails to parse takes the whole theme down: goblog logs `failed to load theme`, falls back to default, and serves default's static files too, so check the log when a theme "does nothing".

Inside a template you have everything `html/template` offers plus one function, `rawHTML`, which inserts a string unescaped — default uses it for `custom_header_code`, `custom_footer_code` and the plugin HTML. Cross-file calls work as usual: `{{ template "header.html" . }}` at the top of a page and `{{ template "footer.html" . }}` at the bottom is how every default page is built, and `header.html` itself begins with `{{ template "_head" . }}`.

Static files are served at `/theme/<path>`: goblog looks in the active theme's `static/` first and in `themes/default/static/` second, so an override-only theme still gets the base stylesheet. There is no merging within a file — if you ship `static/css/goblog.css`, yours is the whole stylesheet.

## A minimal theme

Two files are enough. Copy `themes/default/templates/header.html` from the goblog version you target and change the navigation; add a stylesheet:

```text
ocean/
  goblog-theme.json
  templates/
    header.html
  static/
    css/
      goblog.css
```

`header.html` keeps the same shape as default's — the shared head, then the opening of the page — and everything after it (the wrapper, the footer, the "most recent post" block) still comes from default's `footer.html`:

```html
{{ template "_head" . }}
<body>
  <nav id="navigation">
    <a href="/">{{ .settings.site_logo_letters.Value }}</a>
    {{ range .nav_pages }}<a href="/{{ .Slug }}">{{ .Title }}</a>{{ end }}
    {{ if .is_admin }}<a href="/admin">Admin</a>{{ end }}
    {{ if .logged_in }}<a href="/logout">Logout</a>{{ else }}<a href="/login">Login</a>{{ end }}
  </nav>
  <div class="wrapper">
```

`static/css/goblog.css` replaces default's file outright, so start from a copy of it (it is under 4 KiB) and add your rules:

```css
body { font-family: Georgia, serif; background: #f4f8f4; }
#navigation a { margin-right: 1rem; }
```

`goblog-theme.json` is the manifest the directory reads; the fields are listed under [The manifest](/docs/publishing-a-theme#the-manifest). goblog itself only needs it for **Admin → Themes**, which reads the `display_name` from it.

## Template data

Every template is executed with a map, so keys are reached as `.settings`, `.post` and so on, and a key a handler did not set is simply empty. These keys are on (almost) every render:

| Key | What it is |
|---|---|
| `settings` | The site settings, keyed by setting name; each value has a `.Value`, so `{{ .settings.site_title.Value }}`. Use `{{ with index .settings "robots_tag" }}` for a setting that may not exist. |
| `nav_pages` | The pages with **Show in nav** on, in nav order. Each has `.Slug` and `.Title`. |
| `is_admin`, `logged_in` | Whether the visitor is an admin, or signed in at all. Missing (so false) on most error renders. |
| `title` | The page title default's head uses when there is no `post`. |
| `version` | The running goblog version, for the footer. |
| `recent` | The newest published post, for the "most recent" block in default's footer. |
| `admin_page` | True on admin renders; default's head loads the editor scripts when it is set. |
| `plugin_head_html`, `plugin_footer_html`, `plugins` | The HTML installed plugins inject and their per-template data, keyed by plugin name. Added to every **public** render; admin pages are rendered without them. |

Beyond those, each template gets its own data. This is the contract of the running version, read off the render calls in `blog/blog.go` and `admin/`; a release can add keys, and removals are noted in the changelog.

| Template | Rendered for | Its own keys |
|---|---|---|
| `home.html` | `/` | `recent_posts` (every published post), `tags` (the 20 most used) |
| `post.html` | one post: `/posts/yyyy/mm/dd/<slug>` and `/yyyy/mm/dd/<slug>` for every reader, `/<type>/yyyy/mm/dd/<slug>` for readers who are not admins | `post`, `comments`, `comment_error`, `comment_token`, `comment_user`, `comments_require_login`; on the first two URLs an admin also gets `backlinks`, `outbound_links`, `external_backlinks`, `post_types` |
| `post-admin.html` | `/<type>/yyyy/mm/dd/<slug>` when the reader is an admin, and `/admin/posts/yyyy/mm/dd/<slug>` | `post`, `post_types`, `backlinks`, `outbound_links`, `external_backlinks`; on the public URL also the comment keys |
| `post_type_listing.html` | `/<post type slug>` | `post_type`, `posts` |
| `page_writing.html` | a page of type writing | `page`, `posts` (of the page's post type, or all) |
| `page_tags.html` | a page of type tags | `page`, `tags` |
| `page_archives.html` | a page of type archives | `page`, `yearKeys`, `byYear`, `yearMonthKeys`, `byYearMonth` |
| `page_content.html` | a custom page, and every plugin page (`/docs`, `/plugins`, …) | `page`; a plugin page adds what the plugin returns, normally `has_plugin_content` and `plugin_content`, and may replace `title` |
| `search.html` | `/search` | `posts`, `query` |
| `tag.html` | `/tag/<name>` | `posts`, `tag` |
| `login.html` | `/login` | `client_id`, `next`, `email_login_enabled` |
| `error.html` | 404s, a disabled or uninstalled plugin's page, unauthorized | `error`, `description` |
| `admin*.html` | `/admin/…` | per page: `posts`, `post_types`, `pages`, `comments`, `users`, `themes`, `setting_groups`, `plugin`, … — read `admin/admin.go` and `admin/plugins.go` before overriding one |
| `wizard_*.html` | the install wizard, before there is a database | only `version` and `title` (and `errors`) — none of the common keys exist yet |

Six templates in default — `about.html`, `archives.html`, `posts.html`, `presentations.html`, `projects.html`, `tags.html` — are left over from before pages were configurable; no route renders them today, so there is nothing to override. Post, page, tag and setting objects are goblog's own types: `.post.Title`, `.post.Permalink`, `.post.Tags`, `.post.CreatedAt.Format "Jan 02, 2006"`, `.page.HasHero`, `.page.HeroURL`. Default's templates show what each has; the Go types are in `blog/`.

## Try it locally

Themes are looked up in two roots, built-in first: `themes/` in the working directory, then the installed root, which is `themes/installed/` unless `THEMES_INSTALLED_DIR` points elsewhere. Put your theme at `themes/installed/ocean/` (in Docker, bind-mount `themes/installed/` so it survives a restart). A directory counts as a theme when it has a `templates/` subdirectory and its name matches `^[A-Za-z0-9_-]+$`; `installed` and `shared` are not theme names.

Then activate it, either way:

- **Admin → Settings → Appearance → Theme** lists every theme in both roots; save the form.
- **Admin → Themes** lists it under **Installed** with an *installed* badge — the display name comes from your `goblog-theme.json` — next to the built-ins. Press **Activate**.

Both write the `theme` setting and reload the template set at once, without a restart. Edits to your files are not watched: after changing a template, save the setting again (or activate another theme and back) to re-parse. Static files are read from disk on every request, so CSS changes show on reload.

If the theme fails to parse, the site keeps rendering with default and the reason is in goblog's log. The fallback is silent in the browser, so tail the log while you work.

## Pitfalls

- **An empty file does not blank a template.** Go keeps the earlier definition when a later one is empty, so an empty `footer.html` still renders default's footer. To remove a section, ship a file with content — a comment will do.
- **Undefined template references fail at render, not at load.** `{{ template "nope" . }}` parses fine; the page it is on fails to render, with the error in goblog's log. Loading only checks that each file parses, and so does the directory's validation, so open every page you changed before you tag.
- **256 KiB per template.** Each file under `templates/` must be at most 262144 bytes; the installer and the directory both refuse a larger one. Default's largest template is under 32 KiB.
- **A shipped static file replaces default's file whole.** Fallback is per path, not per rule; a `goblog.css` with one line loses everything default's had.
- **Leave admin and wizard pages to default unless you must.** They change with releases — a new setting, a new tab — and an override pins them to the version you copied. The directory copy of forest ships none of them. The same goes for `_head`: nothing you put in `templates/` replaces `templates/shared/_head.html` by name, and redefining `_head` inside one of your files means maintaining the CDN pins and meta tags yourself.
- **A theme is code.** Once active, your templates render every page — including the admin — with the same functions and data goblog's own get; there is no sandbox around `rawHTML`. See the [trust model](/docs#trust-model).

## Next

To publish it, read [Publishing a theme](/docs/publishing-a-theme): the manifest, the screenshot, `vX.Y.Z` releases, and what the directory checks. The JSON your theme becomes once listed is in [Directory formats](/docs/directory-formats).
