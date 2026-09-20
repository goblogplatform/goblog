# Publishing a goblog plugin

The directory at [goblog.live/plugins](https://goblog.live/plugins) lists plugins that have been submitted at [goblog.live/plugins/submit](https://goblog.live/plugins/submit) and approved by a maintainer. A plugin is a GitHub repository whose releases each carry a compiled WebAssembly module; each GitHub release is a version. Submission checks the repository right away (the checks below) and queues it; once approved, the directory re-checks it every few hours and picks up new releases on its own.

**Only WebAssembly plugins are accepted.** Yaegi-interpreted `.go` plugins (goblog's [dynamic plugins](https://github.com/goblogplatform/goblog#dynamic-plugins)) still work when an operator drops the file in by hand, but the directory does not list or install them: a `.wasm` module can bundle any dependency its author likes, runs with no filesystem and no network beyond the hosts it declares, and needs no goblog rebuild.

## What the directory publishes

The index entry for your plugin is built from the manifest, the latest release and its module asset (the one named by `entry`, `plugin.wasm` by default) (`download_url` is the asset's browser URL; `sha256` is of the asset), and your repository's GitHub star count (`stars`), which the directory uses for its default ordering. Stars are best-effort: if GitHub cannot be reached for them, the entry is published with `0`. The entry also carries `runtime: "wasm"`, `install_type: "wasm"` and the manifest's `allowed_hosts`, which goblog's admin page shows as "Talks to: …" before an operator installs.

## What the repository must contain

At the root of the repository, at the release tag being published (the tool checks the latest release):

| File | Required | Notes |
|---|---|---|
| `goblog-plugin.json` | yes | the manifest, below |
| the plugin's source | yes | anything that builds the module — Go with [`github.com/extism/go-pdk`](https://github.com/extism/go-pdk), TinyGo, Rust, or any language with an [Extism PDK](https://extism.org/docs/concepts/pdk) |
| `README.md` | yes | shown on the plugin's directory page |
| `CHANGELOG.md` | no | shown when present |
| `LICENSE` | recommended | not checked by the validator; state the same license as `license` in the manifest |

And attached to every release: the compiled module, named as `entry` in the manifest (default `plugin.wasm`). The module is a release **asset**, not a file in the repository.

### `goblog-plugin.json`

```json
{
  "name": "hello",
  "display_name": "Hello",
  "description": "One sentence shown in the listing.",
  "author": "Your Name",
  "license": "Apache-2.0",
  "runtime": "wasm",
  "entry": "plugin.wasm",
  "allowed_hosts": ["api.example.com"],
  "min_goblog_version": "0.2.9",
  "homepage": "https://example.com/optional"
}
```

- `name`: `^[a-z0-9-]+$`, unique across the registry, and equal to the `name` your plugin's `identity` export returns. It keys the plugin's settings and its persistent store, so keep it stable across versions.
- `display_name`: the label shown in the directory. It does not have to equal your plugin's `display_name`, which labels its settings group in the admin UI.
- `license`: an SPDX identifier from the list in `plugins/directory/registry/manifest.go` (MIT, Apache-2.0, BSD-2/3-Clause, ISC, MPL-2.0, GPL/LGPL/AGPL `-only`/`-or-later`, Unlicense, 0BSD). Open an issue to add another.
- `runtime`: must be `"wasm"`. Anything else is rejected.
- `entry`: the name of the `.wasm` asset attached to each release (letters, digits, `_`, `.`, `-`; no path); defaults to `plugin.wasm`.
- `allowed_hosts`: the hosts the module may reach over HTTP — exact hostnames (`api.example.com`), IPs, or globs (`*.example.com`), each optionally with a port; never a scheme or a path. A glob must still name a domain — `*` alone (or `**`, `*.*`) is rejected. Omit it, or leave it empty, and the plugin gets no network at all. goblog checks every request (and every redirect hop) against this list, and the directory shows it to operators as "Talks to" before they install, so declare only what you use.
- `min_goblog_version`: plain semver (`0.2.9`, no `v`) — the oldest goblog your plugin works with. WebAssembly plugins need at least `0.2.9`.

### The module

A plugin is one `.wasm` file built for [Extism](https://extism.org/): every export takes and returns JSON through Extism's input/output. Only `identity` is mandatory; the others (`settings`, `pages`, `jobs`, `template_head`, `template_footer`, `template_data`, `render_page`, `run_job`, `on_init`) are optional and mirror goblog's compiled-in plugin interface. Host functions give you a per-plugin key/value store (`store_get`/`store_set`/`store_delete`/`store_list`), logging through the PDK's logger, and Extism's `http_request` limited to `allowed_hosts`. Calls are capped at 10 s (120 s for jobs and `on_init`) and 64 MB of memory. The full contract — every export's input and output shape, `ctx`, the host functions and the limits — is in goblog's README under [WebAssembly plugins](https://github.com/goblogplatform/goblog#webassembly-plugins), and [`plugin/wasm/testdata/echo/main.go`](https://github.com/goblogplatform/goblog/blob/main/plugin/wasm/testdata/echo/main.go) implements all of it. [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello) is the smallest complete example and is meant to be copied.

With the standard Go toolchain (1.24 or newer):

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
```

The module must be 16 MiB or smaller; `-ldflags="-s -w"` keeps a Go build well under that.

### Releases

- Tag releases `vX.Y.Z` (exactly three numbers). Drafts and pre-releases are ignored.
- The tag without `v` must equal the `version` your plugin's `identity` export returns.
- **Every release must have the module attached** as the asset named by `entry`. The registry validates and publishes the asset, never a file from the repository, so a release without it fails validation with `has no asset named plugin.wasm`.
- The GitHub release body is shown as the version's release notes.
- The directory lists the **latest** published release; the detail page shows all of them.

Copy this workflow into `.github/workflows/release.yml` and the asset is built and uploaded whenever you publish a release:

```yaml
name: Release
on:
  release:
    types: [published]
permissions:
  contents: write
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Build plugin.wasm
        run: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
      - name: Upload to the release
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release upload "${{ github.event.release.tag_name }}" plugin.wasm --clobber
```

The directory reads the asset when it validates; if the workflow is still uploading when it looks, submit again once the asset is up.

## Check before you submit

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
docker run --rm --network none -v "$PWD:/p:ro" \
  --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:v0.2.9 \
  validate-plugin /p/plugin.wasm
# {"name":"hello","display_name":"Hello","version":"1.0.0","runtime":"wasm"}
```

The directory runs this same check (plus a timeout and memory limits) against the asset on your latest release when you submit, then requires `"runtime":"wasm"` and compares `name` and `version` with your manifest and tag. `validate-plugin` loads the module with no store and no network and calls `identity`, `settings`, `pages` and `jobs`, so those exports must not depend on either.

## Submit

Paste your repository (`owner/name` or its GitHub URL) at [goblog.live/plugins/submit](https://goblog.live/plugins/submit). The check above runs immediately, before the page reloads — a failure is shown on the spot with the reason; a pass queues the repository under Pending review. A maintainer approves or rejects it from Admin → Plugins → Directory; an approved repository appears in the index within minutes and is re-checked for new releases every few hours after that.

Any goblog can host a directory (turn it on under Admin → Settings → Plugin Directory), so submitting to one instance does not list you on another; goblog.live is the one most installs read from.

Plugins run inside the goblog process of whoever installs them, sandboxed but trusted with the hosts they declare and the settings they are given. Keep them small and readable; the directory is curated and maintainers may decline or delist entries.
