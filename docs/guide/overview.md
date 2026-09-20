# Overview

goblog can be extended in two ways. **Plugins** are WebAssembly modules: sandboxed code that adds settings, HTML, pages and background jobs to a site, installed from the directory with one click. **Themes** are a directory of templates and CSS layered on top of the default theme. [goblog.live](https://www.goblog.live) hosts a curated directory of both, and any goblog can install from it or host its own.

These pages tell you how to write and publish each. If you are here for a specific thing:

- Build your first plugin: [Writing a plugin](/docs/writing-a-plugin).
- Look up an export, a host function or a limit: [Plugin API reference](/docs/plugin-api).
- Restyle a site: [Writing a theme](/docs/writing-a-theme).
- Get listed: [Publishing a plugin](/docs/publishing-a-plugin) and [Publishing a theme](/docs/publishing-a-theme).

## Plugins

A plugin can:

- declare **settings**, which appear under **Admin → Settings** grouped by plugin and reach every call as strings;
- inject HTML into `<head>` and before `</body>` on every rendered page;
- own **pages** of its own: a page type with a slug, an optional nav entry, and everything under that slug;
- run **scheduled jobs** at an interval it chooses;
- keep data in a **persistent key/value store** that survives restarts and updates;
- make **HTTP requests** to the hosts it declares.

It runs inside goblog as an [Extism](https://extism.org/) WebAssembly module. Each hook goblog would call on a compiled-in plugin becomes a call of the matching export, with JSON in and JSON (or HTML) out. The module has no filesystem access and can reach the network only at the hosts in its `allowed_hosts` list — none declared means no network at all. It gets 64 MB of memory, 10 s per template or page call and 120 s per job, and a call that hits its timeout closes the instance rather than stalling the site.

Why WebAssembly? Any language with an Extism PDK works, dependencies are bundled into the module, and nothing about goblog needs to be rebuilt or restarted to install one. The alternative — a Go plugin compiled into the binary — is for things that ship with goblog itself.

Start with [Writing a plugin](/docs/writing-a-plugin); every export, host function and limit is in the [Plugin API reference](/docs/plugin-api).

## Themes

A theme is a directory with `templates/` and, optionally, `static/`. Its templates are loaded **on top of `themes/default`** and override default's by file name, so a theme ships only the templates it changes: everything else, including admin pages added by newer goblog releases, keeps rendering from default. `/theme/<file>` serves the active theme's `static/` and falls back to default's for anything the theme does not ship.

Themes installed from the directory land in `themes/installed/<name>/` (bind-mount that directory in Docker, or installs vanish on restart) and are activated with the `theme` setting, which hot-reloads without a restart. See [Writing a theme](/docs/writing-a-theme).

## Trust model

A plugin is **sandboxed**. The fences are: no filesystem; network only to the hosts it declared, checked on every request and every redirect hop; a memory cap of 64 MB; a timeout on every call; a store capped per plugin; and one loaded instance per plugin, so a slow call cannot take every page down with it. What it is trusted with is exactly what it declares — the hosts it may talk to and the settings an admin types in. The directory publishes that host list in its index, and your own **Admin → Plugins** page shows it as "Talks to: …" (or "No network access") before you click Install.

A theme is **code**. Once activated its templates render every page, including the admin, with the same template functions and data goblog's own templates get. There is no sandbox around a template.

A directory listing is a maintainer's approval, not a code audit. The directory's validation checks that a plugin or theme is well-formed — it loads, its manifest parses, its name and version match — not that it is benign. Install only what you trust, from a directory you trust.

## The directory

Publishing is the same shape for plugins and themes: paste your GitHub repository at the directory's submit page, and it is **validated on the spot** (latest release, manifest, and for a plugin that `plugin.wasm` loads and its name and version match). A pass **queues** it under Pending review; a **maintainer approves** it; and it is **listed** within minutes, then re-checked every few hours for new releases.

What goblog's installer reads is not the HTML listing but the JSON index next to it: `/plugins/index.json` and `/themes/index.json`. Every goblog fetches those from the `plugin_directory_url` and `theme_directory_url` settings, which default to goblog.live. Point them elsewhere to run a private directory — any goblog can host one by enabling its built-in directory plugin — but treat those URLs as a trust decision: whatever they point at can offer code that runs inside your site once you click Install.

Details: [Publishing a plugin](/docs/publishing-a-plugin), [Publishing a theme](/docs/publishing-a-theme), and the index and detail JSON in [Directory formats](/docs/directory-formats).

## Reference implementations

- [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello) — the smallest complete plugin: one setting and a footer greeting. Meant to be copied.
- [goblog-plugin-scholar](https://github.com/goblogplatform/goblog-plugin-scholar) — a plugin with a page, a scheduled job, the store and an outbound HTTP call.
- [goblog-theme-forest](https://github.com/goblogplatform/goblog-theme-forest) — a published theme.
- [`plugin/wasm/testdata/echo/main.go`](https://github.com/goblogplatform/goblog/blob/main/plugin/wasm/testdata/echo/main.go) — goblog's test fixture, which implements every export and host function in the most literal way.
