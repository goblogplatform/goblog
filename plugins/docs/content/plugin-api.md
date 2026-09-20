# Plugin API reference

A goblog plugin is an [Extism](https://extism.org/) module. goblog calls its **exports** through Extism's input/output: the input is a JSON document (empty for the four load-time exports), the output is JSON, or a plain HTML string for the two template hooks. Return `0` from an export on success; set an error with the PDK (`pdk.SetErrorString` in Go) and return non-zero on failure.

Only `identity` is mandatory. goblog checks which of the other exports exist once, when it loads the module, and a missing one behaves exactly like a compiled-in plugin that leaves that hook at its no-op default. This page is the contract; [Writing a plugin](/docs/writing-a-plugin) walks through a complete module, and every snippet below is trimmed from goblog's own test fixture, [`plugin/wasm/testdata/echo/main.go`](https://github.com/goblogplatform/goblog/blob/main/plugin/wasm/testdata/echo/main.go), whose `out` helper is `json.Marshal` + `pdk.Output` + `return 0`.

## Exports

| Export | Input → Output | When goblog calls it |
|---|---|---|
| `identity` | → `{name, display_name, version}` | once, at load |
| `settings` | → `[{key, type, default, label, description}]` | once, at load |
| `pages` | → `[{page_type, title, slug, show_in_nav, nav_order, description}]` | once, at load |
| `jobs` | → `[{name, interval_seconds}]` | once, at load |
| `template_head` / `template_footer` | `ctx` → HTML string | every rendered page |
| `template_data` | `ctx` → JSON object, available as `.plugins.<name>` | every rendered page |
| `render_page` | `ctx` → `{"html": "…"}` **or** `{"template": "…", "data": {…}}` **or** `{"raw": {"status", "content_type", "body"}}` | a request under one of the plugin's pages |
| `run_job` | `{"name", "settings"}` → ignored | on each job's interval |
| `on_init` | `{"settings": {k: v}}` → ignored | at startup and after an install or update |

Under `validate-plugin` the four load-time exports run with no store and no network, so they must not depend on either. An error from any of them fails the load; an error from a hook is logged and treated as if the hook had returned nothing.

### identity

**Input:** none.

**Output:** `{"name": "…", "display_name": "…", "version": "…"}`. `name` and `version` are required — a module without both is rejected with `identity must include name and version`. `name` must match the manifest's `name` (it keys the plugin's settings and store), and `version` must match the release tag without the `v`, or the directory rejects the release.

**What goblog does:** `name` becomes the plugin's identifier everywhere (settings, store, admin, log lines); `display_name` labels its settings group under **Admin → Settings**.

```go
//go:wasmexport identity
func identity() int32 {
	return out(map[string]string{"name": "echo", "display_name": "Echo", "version": "1.2.3"})
}
```

### settings

**Input:** none.

**Output:** an array of setting definitions.

| Field | Meaning |
|---|---|
| `key` | The setting's key: what the admin form posts and what every later call's `settings` map is keyed by. |
| `type` | `text` (a single-line input), `textarea` (a multi-line one) or `password` (an input that renders empty and, left empty on save, keeps the stored value). Anything else renders as `text`. |
| `default` | The initial value. Every value is a string; booleans are `true`/`false`. |
| `label` | Shown next to the input. |
| `description` | Help text under the label. |

**What goblog does:** at startup and after an install or update, every declared key that has no stored value is seeded with its `default`. From then on the stored values are what the admin form edits and what reaches every hook, job and `on_init` call as `settings`. A key you stop declaring keeps its stored value but is no longer shown.

The key `enabled` is special: it is the on/off switch in the plugin's card header under **Admin → Settings**, stored as `true` or `false`. Its effect:

- **Template hooks and jobs:** when you declare `enabled`, goblog skips `template_head`, `template_footer`, `template_data` and `run_job` unless the stored value is `true`. Without an `enabled` setting they always run.
- **Pages:** a page is served, and listed in the nav, only while the owning plugin's stored `enabled` is `true` — with or without a declaration. A plugin that exports `pages` should therefore declare `enabled` (with `default` `true` if it should work out of the box) — a plugin with no settings at all has no card, so no switch — or its pages answer *Page Not Available*.

```go
//go:wasmexport settings
func settings() int32 {
	return out([]map[string]string{
		{"key": "enabled", "type": "text", "default": "true", "label": "Enabled", "description": "on/off"},
		{"key": "greeting", "type": "text", "default": "hi", "label": "Greeting", "description": "footer text"},
	})
}
```

### pages

**Input:** none.

**Output:** an array of page definitions.

| Field | Meaning |
|---|---|
| `page_type` | The page's identifier, unique across all plugins; a second plugin claiming it is not registered. It is how goblog routes a request to `render_page`. |
| `title` | The page's heading and nav label. |
| `slug` | The top-level URL segment: `research` serves `/research` and everything under it. Must match `^[a-z0-9-]+$` and must not be one goblog reserves (`admin`, `api`, `comments`, `login`, `logout`, `posts`, `search`, `sitemap.xml`, `tags`, `test_db`, `theme`, `wizard`, `wizard_db`, `wp-content`). |
| `show_in_nav` | Whether the page starts out in the site navigation. |
| `nav_order` | Its position there (ascending). |
| `description` | Help text; not shown anywhere today. |

**What goblog does:** at startup and after an install or update, it creates a page row for each `page_type` that has none yet, from `title`, `slug`, `show_in_nav` and `nav_order`, enabled. The row is the admin's from then on — title, hero, nav placement and even the slug can be edited under **Admin → Pages** without touching the plugin, because requests are routed by `page_type`. A slug that is invalid, reserved, or already used by a page of another type is logged and no page is created. Uninstalling the plugin deletes the rows it created.

```go
//go:wasmexport pages
func pages() int32 {
	return out([]map[string]any{{"page_type": "echo", "title": "Echo", "slug": "echo", "show_in_nav": true, "nav_order": 5, "description": "echo page"}})
}
```

### jobs

**Input:** none.

**Output:** `[{"name": "…", "interval_seconds": N}]`. An `interval_seconds` of 0 or less means one hour.

**What goblog does:** starts one ticker per job when the plugin is started (after `on_init` at boot, or after an install). The first run is one interval after start, not immediately; each tick calls `run_job` with the job's `name` and the plugin's current settings, skipping the call while `enabled` is declared and not `true`. A failed run is logged as `Plugin <name> job <job> error` and the ticker carries on.

```go
//go:wasmexport jobs
func jobs() int32 {
	return out([]map[string]any{{"name": "tick", "interval_seconds": 60}})
}
```

### template_head and template_footer

**Input:** [`ctx`](#ctx). `request.sub_path` is empty here; `template` is the file being rendered, such as `home.html` or `post.html`.

**Output:** an HTML string — write it with `pdk.OutputString`, not as JSON. Nothing, or an empty string, adds nothing.

**What goblog does:** concatenates every plugin's output, in registration order, into `plugin_head_html` (inside `<head>`) and `plugin_footer_html` (just before `</body>`) and renders them unescaped. The browser trusts whatever you return, so escape anything that came from a setting or the request yourself. These hooks run on every page the site renders and wait at most 2 s for the instance if another call holds it (see [Limits and lifecycle](#limits-and-lifecycle)).

```go
//go:wasmexport template_footer
func templateFooter() int32 {
	c := readCtx()
	pdk.OutputString("<p>" + c.Settings["greeting"] + "</p>")
	return 0
}
```

### template_data

**Input:** [`ctx`](#ctx), the same as the two HTML hooks.

**Output:** a JSON object, or nothing.

**What goblog does:** places the object under `.plugins.<name>` in the data of every template, so a theme can read `{{ .plugins.echo.greeting }}` (or `{{ index .plugins "my-plugin" }}` for a hyphenated name). A plugin that returns nothing has no entry. Like the HTML hooks it is skipped when `enabled` is declared and not `true`, and waits at most 2 s for a busy instance.

```go
//go:wasmexport template_data
func templateData() int32 {
	c := readCtx()
	return out(map[string]any{"greeting": c.Settings["greeting"], "template": c.Template})
}
```

### render_page

**Input:** [`ctx`](#ctx). `template` is the page's `page_type`; `request.sub_path` is the part of the URL after the slug — `""` for `/research`, `data.json` for `/research/data.json`, `a/b` for `/research/a/b` (no leading or trailing slash). Everything under the slug reaches the plugin; nothing else does.

**Output:** exactly one of three shapes:

| Shape | What goblog does |
|---|---|
| `{"html": "…"}` | Renders the theme's page-content template with the page's title as heading and your HTML, unescaped, as the body. |
| `{"template": "x.html", "data": {…}}` | Renders `x.html` from the active theme with goblog's usual page data plus your `data` merged in (your keys win). |
| `{"raw": {"status": 200, "content_type": "…", "body": "…"}}` | Writes the response as is. `status` defaults to 200 and must be 100–599; `content_type` defaults to `text/plain; charset=utf-8`. Use it for JSON, feeds and anything that is not a themed page (there is no way to set headers, so no redirects). |

An empty object, nothing at all, an error, or a `raw` status out of range declines the request and goblog answers 404. If more than one shape is present, `raw` wins over `template`, which wins over `html`. `render_page` waits for the instance without a time bound, unlike the template hooks.

```go
//go:wasmexport render_page
func renderPage() int32 {
	c := readCtx()
	switch c.Request.SubPath {
	case "":
		return out(map[string]any{"html": "<h2>echo</h2>" + c.Settings["greeting"]})
	case "data.json":
		return out(map[string]any{"raw": map[string]any{"status": 200, "content_type": "application/json", "body": `{"ok":true}`}})
	case "tmpl":
		return out(map[string]any{"template": "page_content.html", "data": map[string]any{"plugin_content": "from template", "has_plugin_content": true}})
	}
	return out(map[string]any{})
}
```

### run_job

**Input:** `{"name": "…", "settings": {k: v}}` — the job's `name` from your `jobs` list and the plugin's current settings.

**Output:** ignored. Return `{}` or nothing.

**What goblog does:** calls it on the job's ticker with a 120 s budget, and logs a non-zero return or a timeout. This is the place for slow work — fetching, re-indexing, filling the store — that `render_page` then serves from the store. Nothing else runs on the instance while a job does, so a template hook that arrives meanwhile gives up after 2 s and that render skips the plugin.

```go
//go:wasmexport run_job
func runJob() int32 {
	var in struct {
		Name     string            `json:"name"`
		Settings map[string]string `json:"settings"`
	}
	json.Unmarshal(pdk.Input(), &in)
	storeSet("job_ran", in.Name+"/"+in.Settings["greeting"])
	return out(map[string]any{})
}
```

### on_init

**Input:** `{"settings": {k: v}}` — the plugin's current values, its declared defaults overlaid with what is stored.

**Output:** ignored.

**What goblog does:** calls it once at startup after seeding settings and creating page rows, and again after a hot install or update, with a 120 s budget. At startup a non-zero return is logged as an init error and the plugin stays registered; during an install it fails the install. Use it for one-time migration of stored data between versions; do not depend on it for anything a request needs, since `validate-plugin` never calls it.

```go
//go:wasmexport on_init
func onInit() int32 {
	var in struct {
		Settings map[string]string `json:"settings"`
	}
	json.Unmarshal(pdk.Input(), &in)
	storeSet("init", "1")
	storeSet("init_greeting", in.Settings["greeting"])
	return out(map[string]any{})
}
```

## ctx

The input of `template_head`, `template_footer`, `template_data` and `render_page`. As `render_page` of the echo plugin sees `GET /echo/data.json?v=1`:

```json
{
  "settings": {"enabled": "true", "greeting": "hi"},
  "template": "echo",
  "request": {"path": "/echo/data.json", "sub_path": "data.json", "query": {"v": "1"}, "method": "GET"}
}
```

| Field | Notes |
|---|---|
| `settings` | Every stored setting of the plugin, as strings. Never null; empty when the plugin declares none. |
| `template` | The template file being rendered for the template hooks; the `page_type` for `render_page`. |
| `request.path` | The request's URL path. |
| `request.sub_path` | The path after the page slug for `render_page`; empty for the template hooks. |
| `request.query` | The query string, first value per key. Never null. |
| `request.method` | The HTTP method. |

Decode only what you use — the scholar plugin's `hookInput` has `settings` and `request.sub_path` and nothing else.

## Host functions

goblog adds four store functions to the module's imports in Extism's `extism:host/user` namespace. Logging and HTTP are Extism's own.

### store

A persistent key/value store, private to the plugin (keyed by its `name`), kept in goblog's database. It survives restarts and updates; uninstalling the plugin deletes it. Each function takes and returns Extism memory offsets (`i64`): allocate the arguments with the PDK, pass their offsets, and read a returned offset back.

| Function | Returns |
|---|---|
| `store_get(key)` | The value's offset, or `0` (Extism's null) when the key is missing or on error. |
| `store_set(key, value)` | `0` on success, `1` on failure. |
| `store_delete(key)` | `0` on success, `1` on failure; deleting a missing key succeeds. |
| `store_list(prefix)` | The offset of a JSON array of the plugin's keys with that prefix, sorted ascending (`""` lists all); `[]` on error. |

Limits, enforced by `store_set` and logged at warn level under the plugin's name when hit:

- a key is 1–256 bytes;
- a value is at most 1 MiB;
- a plugin keeps at most 10 000 keys and 16 MiB in total (an overwrite frees its old value first and never adds a row).

Values are bytes, not necessarily strings; the scholar plugin stores JSON. Before the site's database is configured (the setup wizard) and under `validate-plugin`, every write fails and every read is empty, which is why the load-time exports must not touch the store.

In Go the imports and a wrapper look like this:

```go
//go:wasmimport extism:host/user store_get
func hostStoreGet(uint64) uint64

//go:wasmimport extism:host/user store_set
func hostStoreSet(uint64, uint64) uint64

//go:wasmimport extism:host/user store_delete
func hostStoreDelete(uint64) uint64

//go:wasmimport extism:host/user store_list
func hostStoreList(uint64) uint64

// storeGetOK returns the value and whether the key exists: the host returns
// offset 0 (Extism's null) for a missing key.
func storeGetOK(key string) (string, bool) {
	k := pdk.AllocateString(key)
	defer k.Free()
	ptr := hostStoreGet(k.Offset())
	if ptr == 0 {
		return "", false
	}
	return pdk.ParamString(ptr), true
}

func storeSet(key, value string) {
	k := pdk.AllocateString(key)
	defer k.Free()
	v := pdk.AllocateString(value)
	defer v.Free()
	hostStoreSet(k.Offset(), v.Offset())
}
```

### logging

There is no goblog log function: use the PDK's logger, `pdk.Log(pdk.LogInfo, "…")` in Go. Info, warn and error lines reach goblog's log as `plugin <name>: <level>: <message>`; debug and trace are dropped.

```go
pdk.Log(pdk.LogInfo, "hello from echo")
```

### http

Outbound HTTP is Extism's built-in `http_request`, which the Go PDK wraps as `pdk.NewHTTPRequest(...).Send()`. The host allows a request only to a host in the plugin's `allowed_hosts` — the manifest's list, written to the sidecar on install (see [Sidecar and loading](#sidecar-and-loading)) — matched exactly or as a glob such as `*.example.com`. No list means no network. Redirects are checked against the same list on every hop, and stop after 10.

A response body is capped at 8 MB. A request to a host outside the list, a transport failure (DNS, refused connection, TLS) or a body over the cap does not return an error to the module: it aborts the export call, which goblog logs and treats as having returned nothing. Keep fetches in `run_job`, where an abort costs one tick, and serve pages from the store.

```go
// c is the decoded ctx and itoa an integer formatter, both echo's own.
req := pdk.NewHTTPRequest(pdk.MethodGet, c.Request.Query["url"])
resp := req.Send()
return out(map[string]any{"html": "status=" + itoa(int(resp.Status())) + " body=" + string(resp.Body())})
```

## Limits and lifecycle

- **Timeouts:** 10 s per call of `identity`, `settings`, `pages`, `jobs`, `template_head`, `template_footer`, `template_data` and `render_page`; 120 s for `run_job` and `on_init`.
- **Memory:** 64 MB per plugin (1024 WebAssembly pages). Allocating past it fails the call.
- **One instance per plugin, calls serialised.** goblog keeps one loaded instance of each module and runs one export on it at a time. `render_page`, `run_job` and `on_init` wait for their turn without bound. The three template hooks wait at most 2 s and then skip that render — logged once per busy stretch — so a slow page or a long job cannot stall every other page on the site.
- **A timeout closes the instance.** wazero enforces the deadline by closing the module, and everything after that fails until the instance is re-created. The next call re-creates it from the module bytes and carries on, at most once per 30 s; a plugin that keeps timing out declines in between (empty hooks, 404 pages, skipped jobs), which is logged once per closed instance. A guest exit (a Go panic ends in one) is handled the same way, except that the call which finds the instance closed re-creates it and retries once.
- **Admin → Plugins** shows the plugin as `busy` while a call holds the instance and `closed` after a timeout or exit until it is re-created; otherwise no badge.
- **Errors:** a non-zero return from a hook is logged with the message you set and treated as no output. From a load-time export it fails the load: the plugin is not registered, `validate-plugin` exits 1, and the directory rejects the release.
- **No filesystem.** WASI is enabled for the clock, sleep and randomness (real ones, not wazero's fixed defaults), but no directories are mounted.

## Sidecar and loading

goblog loads every `.wasm` file in `plugins/wasm/` at startup and registers it under the `name` from its `identity`; a file that fails to load is logged and skipped, and a name already registered is skipped too. Loading is on by default; `ENABLE_WASM_PLUGINS=false` turns it off, which also disables installing, updating and uninstalling from **Admin → Plugins**.

Next to each module, a sidecar with the same base name declares its hosts:

```
plugins/wasm/scholar.wasm
plugins/wasm/scholar.json    {"allowed_hosts": ["api.semanticscholar.org"]}
```

The sidecar's only field is `allowed_hosts`. No sidecar means no network access; a sidecar that is not valid JSON means the module is skipped. The installer writes it from the manifest for directory installs and removes both files on uninstall; when you copy a module by hand, you write it by hand. In Docker, bind-mount `plugins/wasm/` so both survive a restart.

Installing from the directory downloads the release's `.wasm` asset, verifies its sha256 against the directory index, loads it, checks that `identity` reports the listed name and version, and only then writes it and the sidecar to `plugins/wasm/` and starts it — no restart. An update keeps the plugin's settings and store.

## Worked example: Scholar

[goblog-plugin-scholar](https://github.com/goblogplatform/goblog-plugin-scholar) serves a Research page listing an author's publications from Semantic Scholar. Its `main.go` is a good second read after hello, because it uses three of the mechanisms above together and shows how they fit.

**A page.** `pages` declares one page, `page_type` `research` at slug `research`, and `render_page` serves it. It decodes only `settings` and `request.sub_path` from `ctx`, returns `{}` for any sub-path (so `/research/anything` is a 404), and otherwise answers with `{"html": …}`: a hint when `semantic_scholar_id` is empty, an "unavailable" notice when there is nothing cached and the fetch failed, or the rendered list. Because a page is served only while `enabled` is `true`, `settings` declares `enabled` — with `default` `false`, since the page is useless until an author ID is set.

**A job.** `jobs` declares `refresh` every 3600 s, and `run_job` re-fetches when the cache is older than the `cache_hours` setting. The 120 s budget and the log-only failure mode make the job the right place for the slow, fallible HTTP call; `render_page`, with its 10 s budget and no way to catch a transport failure, fetches only when the store is completely empty and no fetch has failed recently.

**The store.** The plugin imports only `store_get` and `store_set`. The fetched articles live under one key as JSON with their fetch time; a second key records when the last fetch failed, so a broken API is retried on a back-off rather than on every page view. Both survive restarts and plugin updates, and the sidecar lists the one host the fetch needs.
