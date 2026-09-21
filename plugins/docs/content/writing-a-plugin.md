# Writing a plugin

This page walks through [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello), the smallest complete plugin: it appends a greeting to the footer of every page, and has one setting for the text. Everything below is taken from that repository; copy it and change the names.

You need:

- **Go 1.24 or newer** — `//go:wasmexport` and `-buildmode=c-shared` for the `wasip1` target arrived in 1.24. The hello repository itself pins Go 1.25 in its `go.mod`, so with `GOTOOLCHAIN=auto` the right toolchain is fetched for you.
- [`github.com/extism/go-pdk`](https://github.com/extism/go-pdk), the Extism plugin development kit for Go.

You do not need goblog's source, a fork, or a rebuild. A plugin is a separate repository that produces one `.wasm` file. Other languages work too — anything with an [Extism PDK](https://extism.org/docs/concepts/pdk) — but this walkthrough is in Go.

## The manifest

`goblog-plugin.json` sits at the root of the repository and tells the directory what the plugin is. This is hello's:

```json
{
  "name": "hello",
  "display_name": "Hello",
  "description": "Appends a configurable greeting to the footer of every page. The reference goblog WebAssembly plugin.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "runtime": "wasm",
  "entry": "plugin.wasm",
  "allowed_hosts": [],
  "min_goblog_version": "0.2.9",
  "homepage": "https://github.com/goblogplatform/goblog-plugin-hello"
}
```

| Field | Rule |
|---|---|
| `name` | `^[a-z0-9-]+$`, unique across the directory, and **equal to the `name` your `identity` export returns**. It keys the plugin's settings and its store, so never change it between versions. |
| `display_name` | The label in the directory listing. Required. |
| `description` | One sentence for the listing. Required. |
| `author` | Required. |
| `license` | One of exactly these SPDX identifiers: `MIT`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `MPL-2.0`, `Unlicense`, `0BSD`, `GPL-2.0-only`, `GPL-2.0-or-later`, `GPL-3.0-only`, `GPL-3.0-or-later`, `LGPL-2.1-only`, `LGPL-2.1-or-later`, `LGPL-3.0-only`, `LGPL-3.0-or-later`, `AGPL-3.0-only`, `AGPL-3.0-or-later`. |
| `runtime` | Must be `wasm`. Anything else is rejected. |
| `entry` | The name of the `.wasm` asset attached to each release. Defaults to `plugin.wasm`; letters, digits, `_`, `.` and `-` only, ending in `.wasm`, no path. |
| `allowed_hosts` | Hostnames, IPs or globs (`*.example.com`) the module may reach over HTTP, each optionally with a port, never a scheme or path. Empty or omitted means no network. `*` alone is rejected. |
| `min_goblog_version` | Plain semver, three numbers, no `v`: the oldest goblog your plugin works with. WebAssembly plugins need at least `0.2.9`. |
| `homepage` | Optional. |

goblog uses the manifest when it installs from the directory (the `allowed_hosts` list is what it enforces). The module itself carries its identity separately, in the export below, and the two must agree.

## Exports

A goblog plugin is an Extism module. Every hook goblog has for a compiled-in plugin — identity, settings, template HTML, pages, jobs, initialisation — is an **export** with the same name in snake case: `identity`, `settings`, `template_footer`, `render_page` and so on. goblog calls the export with a JSON document as its input and reads back JSON, or a plain string for the HTML hooks.

In Go, an export is a function that:

1. is marked `//go:wasmexport <name>` and takes no arguments;
2. reads its input with `pdk.Input()` (a `[]byte` of JSON) and decodes it;
3. writes its result with `pdk.OutputJSON` or `pdk.OutputString`;
4. returns `0` on success, or calls `pdk.SetErrorString` and returns `1`. For the hooks (`template_head`, `template_footer`, `template_data`, `render_page`, `run_job`) goblog logs the message and treats the call as if it had returned nothing. An error from `identity`, `settings`, `pages` or `jobs` is different: goblog reads those four once, when it loads the module, and an error there fails the load — the plugin is not registered at startup, `validate-plugin` reports the error, and the directory rejects the release.

Only `identity` is mandatory. A missing export is the same as a compiled-in plugin that leaves the hook at its no-op default, so hello implements just three. Here they are, verbatim from `main.go`.

`identity` returns the plugin's name, the label for its settings group in the admin, and its version. The name must match the manifest; the version must match the release tag (without the `v`):

```go
//go:wasmexport identity
func identity() int32 {
	return outputJSON(map[string]string{"name": "hello", "display_name": "Hello", "version": "2.0.0"})
}
```

`outputJSON` is a four-line helper that wraps `pdk.OutputJSON` and turns an encoding error into the `SetErrorString` + `return 1` convention:

```go
func outputJSON(v any) int32 {
	if err := pdk.OutputJSON(v); err != nil {
		pdk.SetErrorString("encode output: " + err.Error())
		return 1
	}
	return 0
}
```

`settings` declares what appears on the plugin's settings page under **Admin → Plugins**. Each entry has a `key`, a `type` (`text` for a single-line input, `textarea` for a multi-line one, or `password` for an input that renders empty and keeps the stored value when saved blank — see [settings](/docs/plugin-api#settings); there is no checkbox type, so booleans are the strings `true` and `false`; the `enabled` key alone gets the page's on/off switch), a `default`, a `label` and a `description`. A setting named `enabled` is special: when you declare one, goblog does not call your template hooks or jobs unless its value is `true`. Hello checks it again anyway, which costs nothing and keeps the module correct on its own.

```go
//go:wasmexport settings
func settings() int32 {
	return outputJSON([]setting{
		{Key: "enabled", Type: "text", Default: "true", Label: "Enabled", Description: "Set to 'true' to show the greeting"},
		{Key: "message", Type: "text", Default: "Hello from a WebAssembly plugin", Label: "Message", Description: "Text shown at the bottom of every page"},
	})
}
```

`template_footer` is called on every rendered page with the plugin's current settings (and the template name and request, which hello ignores) and returns HTML that goblog places just before `</body>`. Note the comment: setting values were typed by an admin, but the browser will trust whatever this function returns, so everything from a setting or a request is escaped before it goes out.

```go
//go:wasmexport template_footer
func templateFooter() int32 {
	var in hookInput
	if err := json.Unmarshal(pdk.Input(), &in); err != nil {
		pdk.SetErrorString("template_footer: " + err.Error())
		return 1
	}
	if in.Settings["enabled"] != "true" {
		return 0
	}
	// Always escape setting values: an admin typed them, but the browser
	// will trust whatever this returns.
	pdk.OutputString(`<p class="text-center text-muted">` + html.EscapeString(in.Settings["message"]) + `</p>`)
	return 0
}
```

`hookInput` and `setting` are plain structs with JSON tags matching the field names above (`settings`, `key`, `type`, `default`, `label`, `description`); the file also has an empty `func main() {}`, because it is `package main` and Go requires one, though nothing runs it. The full list of exports, the exact shape of every input and output, the store and HTTP host functions, and the limits are in the [Plugin API reference](/docs/plugin-api#exports).

## Build

The command is the one in hello's header comment:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
```

`GOOS=wasip1 GOARCH=wasm` targets WebAssembly with the WASI preview 1 interface, and `-buildmode=c-shared` produces a library-style module whose entry points are the `//go:wasmexport` functions, rather than a command that runs `main` and exits. `-ldflags="-s -w"` strips the symbol table and DWARF data; the module must be **16 MiB or smaller** for the directory to accept it and for goblog to install it, and a stripped Go build stays well under that.

To keep ordinary `go test` working on your machine, put `//go:build wasip1` at the top of the file with the exports, and add a second file with the opposite tag that only supplies `main`:

```go
//go:build !wasip1

package main

func main() {}
```

That is how the [scholar plugin](https://github.com/goblogplatform/goblog-plugin-scholar) does it: its parsing and fetching logic lives in tag-free files with normal unit tests that run natively, the exports and host-function imports are in the `wasip1` file, and the release workflow runs `go test ./...` before it builds the module.

## Validate

goblog can load a module in isolation and report what it found:

```bash
./goblog validate-plugin plugin.wasm
# {"name":"hello","display_name":"Hello","version":"2.0.0","runtime":"wasm"}
```

This loads the module with no store and no network, calls `identity`, `settings`, `pages` and `jobs`, and prints the identity as JSON — or exits 1 with the load error on stderr. Those four exports must therefore not depend on the store or the network. Without a goblog binary, use the Docker image; its entrypoint is a shell command, so override it:

```bash
docker run --rm --network none -v "$PWD:/p:ro" \
  --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:latest \
  validate-plugin /p/plugin.wasm
```

This is the same check the directory runs on every submission.

## Run it locally

goblog loads every `.wasm` file in `plugins/wasm/` at startup (loading is on by default; `ENABLE_WASM_PLUGINS=false` turns it off). Copy the module there:

```bash
cp plugin.wasm plugins/wasm/hello.wasm
```

If the plugin needs the network, write a sidecar with the same base name declaring its hosts — with no sidecar it gets none. Hello needs nothing, but a plugin that talks to an API would ship:

```json
{"allowed_hosts": ["api.example.test"]}
```

as `plugins/wasm/hello.json`. (The installer writes this file for you when you install from the directory; by hand, you write it.) In Docker, bind-mount `plugins/wasm/` so the files survive a restart.

Restart goblog. The plugin appears under **Admin → Plugins** and its settings under **Admin → Plugins → Hello → Settings**. Hello's `enabled` setting defaults to `true`, so the greeting is already at the bottom of every page — the last thing before `</body>`, after the theme's footer. Change `message` and reload to see it update; set `enabled` to `false` to hide it.

## Next

Hello only uses the template hook. The [Plugin API reference](/docs/plugin-api) covers the rest: [`pages` and `render_page`](/docs/plugin-api#pages) to own a URL, [`jobs` and `run_job`](/docs/plugin-api#jobs) to run work on a schedule, and the [`store_get`/`store_set`/`store_delete`/`store_list` host functions](/docs/plugin-api#store) for data that outlives a request. The scholar plugin uses all three.

When it works, tag a release and submit the repository: [Publishing a plugin](/docs/publishing-a-plugin).
