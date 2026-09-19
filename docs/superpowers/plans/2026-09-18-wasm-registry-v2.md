# WASM Registry Contract v2 + hello v2 (sub-project B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The plugin registry lists WebAssembly plugins only — manifest `runtime: wasm`, the `.wasm` shipped as a release asset, declared `allowed_hosts` — and `goblogplatform/goblog-plugin-hello` v2.0.0 is the first such plugin.

**Architecture:** `goblog-plugin-hello` is rewritten as a standard-Go WASM plugin with a release workflow that builds and uploads `plugin.wasm`. In the registry, `Release` carries its assets, `Source` gains `ReleaseAsset`, `ValidateEntry` downloads the asset named by the manifest's `entry` and validates it with `goblog validate-plugin plugin.wasm` in the pinned `compscidr/goblog:v0.2.9` image; `Build` emits `runtime`, `allowed_hosts`, `install_type: wasm` and the asset's browser download URL.

**Tech Stack:** Go (go-github v92: `RepositoryRelease.Assets`, `DownloadReleaseAsset`), `github.com/extism/go-pdk` v1.1.3 in the hello repo, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-18-wasm-plugins-design.md` §3.

## Global Constraints

- Image pin everywhere: `compscidr/goblog:v0.2.9` (workflows, `cmd/registry/main.go` default, `docs/CONTRACT.md`, `validator_test.go`).
- Manifest v2: `runtime` required and must be `"wasm"`; `entry` defaults to `plugin.wasm`, must end in `.wasm`, no `/`; `allowed_hosts` optional list, each `^[A-Za-z0-9.*:-]+$` (hostnames / IPs / globs, no scheme or path); other fields unchanged. A manifest without `runtime: wasm` fails with "the directory only lists WebAssembly plugins; see docs/CONTRACT.md".
- The entry is a **release asset** of the validated release (name == `entry`); missing → `release vX.Y.Z has no asset named plugin.wasm`; asset ≤ 16 MiB.
- Index entry: `download_url` = the asset's `browser_download_url`; `sha256` of the asset bytes; `install_type: "wasm"`, `runtime: "wasm"`, `allowed_hosts: [...]` (empty array, never null).
- `validate-plugin` output must report `"runtime":"wasm"`; name/version identity checks unchanged.
- hello v2: identity `hello` / `Hello` / `2.0.0`, settings `enabled` (default `true`) and `message` (default `Hello from a WebAssembly plugin`), `template_footer` renders `<p class="text-center text-muted">` + HTML-escaped message when enabled; built with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .`; release workflow uploads the asset on `release: published`.
- Repos: `~/dev/goblog-plugin-hello` (branch `feat/wasm`, PR, then release `v2.0.0` after merge), `~/dev/goblog-plugins` (branch `feat/wasm-contract`, one PR). Never push to `main`; never merge; commit trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`; `git branch --show-current` before every commit.
- Toolchain quirks on this machine: prefix `go` with `GOTOOLCHAIN=go1.26.1` when it complains; `TMPDIR=$HOME/.cache/goblog-registry` for anything that runs Docker.

---

### Task 1: hello v2.0.0 as a WASM plugin

**Files (in `~/dev/goblog-plugin-hello`):**
- Create: `go.mod`, `main.go`, `.github/workflows/release.yml`, `.gitignore`
- Delete: `plugin.go`
- Modify: `goblog-plugin.json`, `README.md`, `CHANGELOG.md`

- [ ] **Step 1: Rewrite**

`go.mod`:
```
module github.com/goblogplatform/goblog-plugin-hello

go 1.25.0

toolchain go1.26.1

require github.com/extism/go-pdk v1.1.3
```

`main.go`:
```go
// Hello is the reference goblog WebAssembly plugin: it appends a
// configurable greeting to the footer of every page.
//
// Build:  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
// Every export takes JSON on stdin (pdk.Input) and returns JSON or HTML
// (pdk.Output). See goblog's README "WebAssembly plugins" for the contract.
package main

import (
	"encoding/json"
	"html"

	pdk "github.com/extism/go-pdk"
)

// hookInput is the ctx goblog passes to template hooks; only settings are
// needed here.
type hookInput struct {
	Settings map[string]string `json:"settings"`
}

//go:wasmexport identity
func identity() int32 {
	pdk.OutputString(`{"name":"hello","display_name":"Hello","version":"2.0.0"}`)
	return 0
}

//go:wasmexport settings
func settings() int32 {
	pdk.OutputString(`[` +
		`{"key":"enabled","type":"text","default":"true","label":"Enabled","description":"Set to 'true' to show the greeting"},` +
		`{"key":"message","type":"text","default":"Hello from a WebAssembly plugin","label":"Message","description":"Text shown at the bottom of every page"}` +
		`]`)
	return 0
}

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

func main() {}
```

`.gitignore`: `plugin.wasm`

`goblog-plugin.json`:
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

`.github/workflows/release.yml`:
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

`README.md`:
````markdown
# goblog-plugin-hello

The reference [goblog](https://github.com/goblogplatform/goblog) WebAssembly plugin. It appends a configurable greeting to the footer of every page — the smallest thing that proves a plugin is loaded.

## Install

From your goblog's **Admin → Plugins → Browse**, search for *Hello* and click **Install** (goblog 0.2.9 or newer). Or download `plugin.wasm` from the [latest release](https://github.com/goblogplatform/goblog-plugin-hello/releases/latest) into `plugins/wasm/` and restart goblog.

## Settings

Under **Admin → Settings → Hello**:

| Setting | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Set to `false` to hide the greeting |
| `message` | `Hello from a WebAssembly plugin` | The text shown at the bottom of every page |

## Build it yourself

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
```

Needs Go 1.24 or newer. The plugin talks to no network (`allowed_hosts` is empty), stores nothing, and only implements the `identity`, `settings` and `template_footer` exports.

## Use it as a template

Copy this repository, change the identity (`name` must be unique — it keys the plugin's settings) and follow the [plugin directory contract](https://github.com/goblogplatform/plugins/blob/main/docs/CONTRACT.md). Tag a release as `vX.Y.Z`; the workflow builds and uploads `plugin.wasm` for you.

## License

Apache-2.0.
````

`CHANGELOG.md` — prepend:
```markdown
## 2.0.0

- Rewritten as a WebAssembly plugin (sandboxed; installable from the directory in goblog ≥ 0.2.9). The 1.x Yaegi `.go` file is no longer shipped.
```

- [ ] **Step 2: Build and validate locally**

```bash
cd ~/dev/goblog-plugin-hello && git checkout -b feat/wasm main
GOTOOLCHAIN=go1.26.1 go mod tidy
GOOS=wasip1 GOARCH=wasm GOTOOLCHAIN=go1.26.1 go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm . && ls -la plugin.wasm
mkdir -p $HOME/.cache/goblog-registry/hello && cp plugin.wasm $HOME/.cache/goblog-registry/hello/
docker run --rm --network none -v "$HOME/.cache/goblog-registry/hello:/p:ro" --entrypoint /go/src/github.com/compscidr/goblog/goblog compscidr/goblog:v0.2.9 validate-plugin /p/plugin.wasm
```
Expected: `{"name":"hello","display_name":"Hello","version":"2.0.0","runtime":"wasm"}`. (If the `v0.2.9` image isn't on Docker Hub yet, use goblog's local build: `cd ~/dev/goblog && go run . validate-plugin $HOME/.cache/goblog-registry/hello/plugin.wasm`.)

Also drop it into a local goblog to see the footer: copy to `~/dev/goblog/plugins/wasm/hello.wasm`, run goblog, `curl localhost:7000/ | grep 'Hello from a WebAssembly plugin'`, then remove the file.

- [ ] **Step 3: Commit, PR, then (after the maintainer merges) release**

```bash
git add -A && git commit -m "Rewrite as a WebAssembly plugin (v2.0.0)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/wasm
gh pr create --base main --title "Rewrite as a WebAssembly plugin (v2.0.0)" --body "..."
```
After merge: `gh release create v2.0.0 --title v2.0.0 --notes "Rewritten as a WebAssembly plugin; install from Admin → Plugins (goblog ≥ 0.2.9)."`, then `gh run watch` the `Release` workflow and `gh release view v2.0.0 --json assets --jq '.assets[].name'` → `plugin.wasm`.

---

### Task 2: Registry — manifest v2, release assets, wasm validation, index fields

**Files (in `~/dev/goblog-plugins`, branch `feat/wasm-contract`):**
- Modify: `internal/registry/manifest.go` (+test), `source.go` (+test, `fakeGitHub`), `validate.go` (+test, `memSource`), `validator.go` (+test), `build.go` (+test), `cmd/registry/main_test.go` (`memSource`), `cmd/registry/main.go` (image default), `.github/workflows/{validate,publish,submit}.yml` (image), `docs/CONTRACT.md`, `README.md`

**Interfaces:**
- `Manifest` gains `Runtime string \`json:"runtime"\``, `AllowedHosts []string \`json:"allowed_hosts"\``.
- `type Asset struct{ ID int64; Name string; Size int; DownloadURL string }`; `Release.Assets []Asset`.
- `Source.ReleaseAsset(ctx, owner, repo string, assetID int64) ([]byte, error)` (≤ 16 MiB).
- `Info` gains `Runtime string \`json:"runtime"\``; `DockerValidator` writes and validates `plugin.wasm`.
- `Validated.Entry` = asset bytes; `Validated.Asset Asset`.
- `IndexEntry` gains `Runtime`, `AllowedHosts []string` (json `runtime`, `allowed_hosts`).

- [ ] **Step 1: Failing tests**

`manifest_test.go`: update `goodManifest` to include `"runtime": "wasm", "entry": "plugin.wasm", "allowed_hosts": ["api.example.test"]`; `TestParseManifest_Good` asserts `Runtime == "wasm"`, `Entry == "plugin.wasm"`, `AllowedHosts` length 1; `TestParseManifest_EntryDefaultsToPluginGo` → renamed `..._EntryDefaultsToPluginWasm` expecting `plugin.wasm`; error cases add `"missing runtime"` (remove the field → error containing "WebAssembly"), `"go runtime"` (`"runtime": "go"`), `"entry not wasm"` (`plugin.go`), `"bad host"` (`"allowed_hosts": ["https://x"]`).

`source_test.go`: releases fixture gets `"assets":[{"id":11,"name":"plugin.wasm","size":3,"browser_download_url":"https://github.com/o/r/releases/download/v1.1.0/plugin.wasm"}]` on v1.1.0; handler `GET /repos/o/r/releases/assets/11` writes bytes `wasm` with `Content-Type: application/octet-stream`; test asserts `rels[0].Assets[0].Name == "plugin.wasm"`, `DownloadURL` set, and `src.ReleaseAsset(ctx,"o","r",11)` returns `"wasm"`; unknown id → error.

`validate_test.go` `memSource`: add `assets map[int64][]byte` and `ReleaseAsset`; `helloSource()`'s v1.1.0 release gets `Assets: []Release{...}` → `[]Asset{{ID: 11, Name: "plugin.wasm", Size: len(helloWasm), DownloadURL: "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm"}}` with `helloWasm = []byte("\x00asm hello v1.1.0")`; `helloValidator()` keyed on `sum(helloWasm)` returning `Info{Name:"hello", DisplayName:"Hello", Version:"1.1.0", Runtime:"wasm"}`; drop the `plugin.go` file entry. New error cases: `"no asset"` (remove Assets → error containing "no asset named plugin.wasm"), `"asset too big"` (Size > 16 MiB → error containing "16"), `"not wasm runtime"` (validator returns `Runtime: ""` → error containing "runtime"). `TestValidateEntry_Good` asserts `v.Asset.DownloadURL` and `v.SHA256 == sum(helloWasm)`.

`validator_test.go`: `TestDockerValidator_CommandShape` expects `validate-plugin /p/plugin.wasm`; `TestDockerValidator_Real` uses the real `echo.wasm` from goblog (`../../../goblog/plugin/wasm/testdata/echo.wasm` relative to the package — read it; skip if absent) with image `compscidr/goblog:v0.2.9` and asserts `Runtime == "wasm"`.

`build_test.go`: `want` entry gets `DownloadURL: "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm"`, `InstallType: "wasm"`, `Runtime: "wasm"`, `AllowedHosts: []string{"api.example.test"}`, `SHA256: sum(helloWasm)`; a second entry without hosts must serialise `"allowed_hosts": []` (assert the raw JSON contains `"allowed_hosts": []`).

`cmd/registry/main_test.go`: fixture manifest gains `"runtime":"wasm"`; release gets an asset; `memSource.ReleaseAsset` returns fixed bytes; `okValidator` returns `Runtime: "wasm"`.

Run: `GOTOOLCHAIN=go1.26.1 go test ./... 2>&1 | head` → FAIL to compile.

- [ ] **Step 2: Implement**

`manifest.go`:
```go
	Runtime      string   `json:"runtime"`
	AllowedHosts []string `json:"allowed_hosts"`
```
`ParseManifest`: default `Entry = "plugin.wasm"`; checks: `if m.Runtime != "wasm" { problems = append(problems, "runtime must be \"wasm\": the directory only lists WebAssembly plugins; see docs/CONTRACT.md") }`; entry `^[A-Za-z0-9_.-]+\.wasm$` ("entry must be a .wasm release asset name"); each host `^[A-Za-z0-9.*:-]+$` and non-empty ("allowed_hosts entries must be hostnames, IPs or globs without scheme or path"); `if m.AllowedHosts == nil { m.AllowedHosts = []string{} }`.

`source.go`:
```go
// Asset is a file attached to a GitHub release.
type Asset struct {
	ID          int64
	Name        string
	Size        int
	DownloadURL string // browser_download_url
}
```
`Release.Assets []Asset`; in `Releases()` map `r.Assets` (`a.GetID()`, `a.GetName()`, `a.GetSize()`, `a.GetBrowserDownloadURL()`). Interface + impl:
```go
	// ReleaseAsset downloads a release asset by id (at most MaxAssetBytes).
	ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error)
```
```go
const MaxAssetBytes = 16 << 20

func (g *GitHubSource) ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error) {
	rc, _, err := g.client.Repositories.DownloadReleaseAsset(ctx, owner, repo, assetID, http.DefaultClient)
	if err != nil {
		return nil, fmt.Errorf("download asset %d of %s/%s: %w", assetID, owner, repo, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download asset %d of %s/%s: %w", assetID, owner, repo, err)
	}
	if len(b) > MaxAssetBytes {
		return nil, fmt.Errorf("asset %d of %s/%s exceeds %d bytes", assetID, owner, repo, MaxAssetBytes)
	}
	return b, nil
}
```
(In the httptest fake, `DownloadReleaseAsset` first GETs the API path with `Accept: application/octet-stream`; serve the bytes there with status 200 and no redirect.)

`validate.go`: after the manifest + README checks, find the asset:
```go
	var asset *Asset
	for i := range latest.Assets {
		if latest.Assets[i].Name == manifest.Entry {
			asset = &latest.Assets[i]
		}
	}
	if asset == nil {
		return nil, fmt.Errorf("%s: release %s has no asset named %s (the release workflow must upload it)", repo, latest.Tag, manifest.Entry)
	}
	if asset.Size > MaxAssetBytes {
		return nil, fmt.Errorf("%s@%s: asset %s is %d bytes; the limit is %d (16 MiB)", repo, latest.Tag, asset.Name, asset.Size, MaxAssetBytes)
	}
	entry, err := src.ReleaseAsset(ctx, owner, name, asset.ID)
	...
	info, err := val.Validate(ctx, entry)
	...
	if info.Runtime != "wasm" {
		return nil, fmt.Errorf("%s@%s: %s is not a WebAssembly plugin (runtime %q)", repo, latest.Tag, manifest.Entry, info.Runtime)
	}
```
`Validated` gains `Asset Asset`; the `File` fetch of `manifest.Entry` is removed.

`validator.go`: `Info.Runtime string \`json:"runtime"\``; write `plugin.wasm`; args end `"validate-plugin", "/p/plugin.wasm"`; comment updated.

`build.go`: `IndexEntry` gains `Runtime string \`json:"runtime"\``, `AllowedHosts []string \`json:"allowed_hosts"\``; `buildDetail` sets `DownloadURL: v.Asset.DownloadURL`, `InstallType: "wasm"`, `Runtime: "wasm"`, `AllowedHosts: v.Manifest.AllowedHosts` (never nil).

Image pin → `compscidr/goblog:v0.2.9` in `cmd/registry/main.go`, the three workflows, `docs/CONTRACT.md`, `validator_test.go`.

- [ ] **Step 3: Docs**

`docs/CONTRACT.md` — rewrite for wasm: repository must contain `goblog-plugin.json` (fields incl. `runtime: "wasm"`, `entry`, `allowed_hosts`), `README.md`, source, `LICENSE`; releases `vX.Y.Z` **with `plugin.wasm` attached** (show the copy-paste `release.yml` from Task 1 and the build command); host contract summary with a link to goblog's README section; `allowed_hosts` semantics (exact host or glob; empty = no network; shown to users as "Talks to"); local check `docker run … validate-plugin /p/plugin.wasm` (expect `"runtime":"wasm"`); submission via the issue form unchanged. Say plainly: `.go` (Yaegi) plugins are not accepted in the directory.
`README.md` — update the index description (`runtime`, `allowed_hosts`, `download_url` is the release asset).

- [ ] **Step 4: Verify, PR**

`gofmt -l . && GOTOOLCHAIN=go1.26.1 go vet ./... && GOTOOLCHAIN=go1.26.1 go test ./...` → PASS. Real run (after hello v2.0.0 is released with its asset): `TMPDIR=$HOME/.cache/goblog-registry GITHUB_TOKEN=$(gh auth token) GOTOOLCHAIN=go1.26.1 go run ./cmd/registry build --out /tmp/dist-v2` → `hello: built`; `dist/index.json` shows `"runtime": "wasm"`, `"install_type": "wasm"`, `download_url` ending `/releases/download/v2.0.0/plugin.wasm`, `sha256` equal to `sha256sum` of the downloaded asset.

Commit(s), push `feat/wasm-contract`, `gh pr create` ("Registry contract v2: WebAssembly plugins with release assets"), `gh pr checks --watch` → `Validate` passes against live hello v2.0.0.

---

### Task 3: Post-merge verification

- [ ] After the maintainer merges the registry PR: watch `Publish index`, then `curl -fsS https://goblogplatform.github.io/plugins/index.json` → one entry `hello 2.0.0` with `runtime: wasm`; download the asset from `download_url` and compare `sha256sum` with the index; `curl https://www.goblog.live/plugins` shows `wasm` and "Talks to: no network"/empty (whatever the listing renders for no hosts). Install from a goblog running v0.2.9 (local is fine: `go run .` with `plugin_directory_url` default) via `POST /api/v1/plugins/install {"name":"hello"}` → 200 and the footer greeting appears.
