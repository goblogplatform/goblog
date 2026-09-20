# Database-backed plugin directory — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** goblog.live's database becomes the plugin registry: repos are submitted on `/plugins/submit`, validated in-process, approved in Admin → Plugins, and the `directory` plugin builds and serves `index.json` itself. `goblogplatform/plugins` is retired.

**Architecture:** A new `plugins/directory/registry` package (ported from the registry repo's `internal/registry`) validates a GitHub repo and builds its detail document, using a `net/http` GitHub client and `wasm.LoadBytes` as the validator. `plugins/directory` gains two gorm tables (`directory_repos`, `directory_builds`), a `Service` with submit/approve/reject/rebuild operations and an in-memory `index.json` cache, and renders everything from the DB. The installer keeps fetching `goblog.live/plugins/index.json`; its `Fetcher` moves into `plugin/installer`. Admin gets seven `/api/v1/directory/*` endpoints and a Directory tab.

**Tech Stack:** Go 1.24, gin, gorm (sqlite in tests), Extism via `plugin/wasm`, `html/template`, vanilla JS in the admin template. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-09-20-db-backed-plugin-directory-design.md`

## Global Constraints

- The index / detail JSON field names are the contract consumed by every goblog's installer: `name, display_name, description, version, author, license, source_url, download_url, sha256, min_goblog_version, install_type, runtime, allowed_hosts, released_at, detail_url, stars` (+ `readme_html, changelog_html, releases[]` on the detail doc). Never rename them. `allowed_hosts` is `[]`, never `null`.
- `MaxAssetBytes = 16 << 20`; the validator's identity check runs inside `wasm.LoadBytes` with no store and no network.
- Settings: `enabled` default `"false"`; `refresh_minutes` default `"360"`, clamped to a minimum of 15; `github_token` default `""`, type `"password"`. `index_url` is removed.
- Public submission limits: 5 submissions per hour per client IP, one validation at a time (`TryLock` → 429), 90 s budget per validation.
- `index.json` is `[]` with HTTP 200 when nothing is approved; `Cache-Control: public, max-age=300`.
- Every error string the submitter sees comes from `registry` validation (same text CI produced) or one of the `directory.Err*` sentinels.
- Follow the repo conventions: comments explain *why*, tests are table-driven where the originals are, `go vet ./...` and `go test ./...` must pass at the end of every task. Commit after every task; never push to `main`.
- Branch: `feat/db-backed-plugin-directory` off `main` (the spec branch `docs/db-backed-plugin-directory-spec` is merged into it first, or the spec commit cherry-picked, so the spec travels with the code).

## Deviations from the spec (decided while planning; no user-visible effect)

- `directory_builds` stores the detail document as one JSON column (`doc`) plus the indexed columns `name`, `version`, `stars`, `built_at`, `last_attempt_at`, `last_error` instead of one column per index field. Nothing queries the individual fields.
- The wire types `Entry`/`Detail`/`Release` stay in `plugins/directory` as aliases of the registry package's `IndexEntry`/`DetailDoc`/`ReleaseDoc` (the directory publishes the contract, so it owns the types); only the `Fetcher` moves to `plugin/installer`. `directory.ValidName` stays.
- Rate limiting is an in-memory per-IP window (counts every attempt, success or failure); `submitter_ip` is still stored for the admin and blanked after 7 days.

## File structure

```
plugins/directory/registry/          NEW — validate + build one repo (ported)
  manifest.go, manifest_test.go        goblog-plugin.json rules
  source.go, source_test.go            Source interface + net/http GitHubSource + fakeGitHub
  validator.go, validator_test.go      Validator interface, WasmValidator, FakeValidator
  validate.go, validate_test.go        ValidateEntry, latestRelease, memSource fixture
  build.go, build_test.go              IndexEntry/DetailDoc/ReleaseDoc, BuildRepo, LatestVersion
plugins/directory/
  index.go                             MODIFY — type aliases onto registry types
  store.go, store_test.go              NEW — Repo/Build models, Migrate, queries
  limiter.go, limiter_test.go          NEW — per-IP submission window
  service.go, service_test.go          NEW — Submit/Add/Approve/Reject/Delist/Rebuild/RefreshAll/Index/Detail
  directory.go, directory_test.go      MODIFY — settings, OnInit, jobs, RenderPage from the Service
  submit.go, submit_test.go            NEW — /plugins/submit GET/POST + ParseRepo
  render.go                            MODIFY — renderSubmit
  templates/listing.html               MODIFY — link to the submit page instead of the GitHub box
  templates/submit.html                NEW
  fetcher.go, fetcher_test.go          DELETE (moved)
plugin/installer/
  fetcher.go, fetcher_test.go          NEW (moved from plugins/directory; type Fetcher)
  installer.go                         MODIFY — *Fetcher, Status.DirectoryHosted
admin/
  directory.go, directory_test.go      NEW — 7 handlers
  admin.go, plugins.go                 MODIFY — Directory field; directory_hosted in status
themes/default/templates/
  admin_plugins.html                   MODIFY — Directory tab
  admin_settings.html                  MODIFY — password input type
goblog.go                              MODIFY — wiring + routes
docs/PLUGIN_CONTRACT.md                NEW (moved from the registry repo)
README.md                              MODIFY — directory section
```

---

### Task 1: `registry` package — manifest rules

**Files:**
- Create: `plugins/directory/registry/manifest.go`, `plugins/directory/registry/manifest_test.go`
- Source to port: `~/dev/goblog-plugins/internal/registry/manifest.go`, `manifest_test.go`

**Interfaces:**
- Produces: `type Manifest struct{Name, DisplayName, Description, Author, License, Runtime, Entry string; AllowedHosts []string; MinGoblogVersion, Homepage string}`, `func ParseManifest(b []byte) (Manifest, error)`, `var NamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)`.

- [ ] **Step 1: Create the branch**

```bash
cd ~/dev/goblog && git checkout main && git pull && git checkout -b feat/db-backed-plugin-directory && git merge --no-edit docs/db-backed-plugin-directory-spec
```

- [ ] **Step 2: Copy the manifest test and its fixture**

```bash
mkdir -p plugins/directory/registry
cp ~/dev/goblog-plugins/internal/registry/manifest_test.go plugins/directory/registry/manifest_test.go
```

Keep the file as-is (package `registry`; `goodManifest` constant; tests `TestParseManifest_Good`, `TestParseManifest_EntryDefaultsToPluginWasm`, `TestParseManifest_Errors` etc.).

- [ ] **Step 3: Run the tests to see them fail to compile**

Run: `go test ./plugins/directory/registry/`
Expected: FAIL — `undefined: ParseManifest`.

- [ ] **Step 4: Copy `manifest.go` and drop the `x/mod` dependency**

```bash
cp ~/dev/goblog-plugins/internal/registry/manifest.go plugins/directory/registry/manifest.go
```

Then edit: replace the package comment with

```go
// Package registry validates goblog plugin repositories and builds the
// directory's index and detail documents. It is what the goblogplatform/
// plugins registry tool used to do in CI; goblog.live now does it itself
// from the directory plugin.
package registry
```

remove the `"golang.org/x/mod/semver"` import, add

```go
// versionPattern is the min_goblog_version rule: plain semver, three
// numbers, no leading v and no pre-release suffix.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
```

and replace the `min_goblog_version` check with

```go
	if !versionPattern.MatchString(m.MinGoblogVersion) {
		problems = append(problems, "min_goblog_version must be a plain semver like 0.2.6")
	}
```

In the `runtime` error message replace `see docs/CONTRACT.md` with `see docs/PLUGIN_CONTRACT.md`.

- [ ] **Step 5: Run the tests**

Run: `go test ./plugins/directory/registry/ -run TestParseManifest -v`
Expected: PASS for every `TestParseManifest_*`. If a test in the copied file asserts the old `docs/CONTRACT.md` string, update it to `docs/PLUGIN_CONTRACT.md`.

- [ ] **Step 6: Commit**

```bash
git add plugins/directory/registry && git commit -m "registry: port manifest rules into goblog

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `registry` package — GitHub source on `net/http`

**Files:**
- Create: `plugins/directory/registry/source.go`, `plugins/directory/registry/source_test.go`
- Source to port: `~/dev/goblog-plugins/internal/registry/source_test.go` (the `fakeGitHub` server is reused verbatim)

**Interfaces:**
- Produces:
  ```go
  var ErrNotFound = errors.New("not found")
  const MaxAssetBytes = 16 << 20
  type Asset struct{ ID int64; Name string; Size int; DownloadURL string }
  type Release struct{ Tag, Name, Body, URL string; PublishedAt time.Time; Draft, Prerelease bool; Assets []Asset }
  type Source interface {
      Releases(ctx, owner, repo string) ([]Release, error)
      File(ctx, owner, repo, ref, path string) ([]byte, error)
      ReleaseAsset(ctx, owner, repo string, assetID int64) ([]byte, error)
      RenderMarkdown(ctx, ownerRepo, markdown string) (string, error)
      RepoStars(ctx, owner, repo string) (int, error)
  }
  func NewGitHubSource(token, baseURL string) *GitHubSource   // baseURL "" → https://api.github.com
  func (g *GitHubSource) SetUserAgent(ua string)
  ```

- [ ] **Step 1: Copy the source test**

```bash
cp ~/dev/goblog-plugins/internal/registry/source_test.go plugins/directory/registry/source_test.go
```

Edit `TestGitHubSource`: `NewGitHubSource` no longer returns an error —

```go
	srv := fakeGitHub(t)
	src := NewGitHubSource("", srv.URL)
	ctx := context.Background()
```

Append two tests:

```go
func TestGitHubSource_SendsTokenAndUserAgent(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"stargazers_count":1}`))
	}))
	t.Cleanup(srv.Close)
	src := NewGitHubSource("tok", srv.URL)
	src.SetUserAgent("goblog-directory/test")
	if _, err := src.RepoStars(context.Background(), "o", "r"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotUA != "goblog-directory/test" {
		t.Errorf("headers: auth=%q ua=%q", gotAuth, gotUA)
	}
}

func TestGitHubSource_RateLimitIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	src := NewGitHubSource("", srv.URL)
	_, err := src.Releases(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "github_token") {
		t.Errorf("want a rate-limit error naming github_token, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/registry/ -run TestGitHubSource`
Expected: FAIL — `undefined: NewGitHubSource`.

- [ ] **Step 3: Write `source.go`**

```go
package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotFound is returned by Source.File when the ref or path does not exist.
var ErrNotFound = errors.New("not found")

// MaxAssetBytes is the largest release asset the registry will download and
// validate (16 MiB); goblog's installer applies the same cap.
const MaxAssetBytes = 16 << 20

// maxAPIBytes caps a JSON or markdown response from the API.
const maxAPIBytes = 8 << 20

// Asset is a file attached to a GitHub release.
type Asset struct {
	ID          int64
	Name        string
	Size        int
	DownloadURL string // browser_download_url
}

// Release is one GitHub release of a plugin repository.
type Release struct {
	Tag         string
	Name        string
	Body        string // release notes, markdown
	URL         string
	PublishedAt time.Time
	Draft       bool
	Prerelease  bool
	Assets      []Asset
}

// Source is what the registry needs from GitHub. It is an interface so the
// validator and builder are tested against an httptest fake.
type Source interface {
	// Releases lists all releases, newest first as GitHub returns them,
	// including drafts and pre-releases (callers filter).
	Releases(ctx context.Context, owner, repo string) ([]Release, error)
	// File returns the contents of path at ref; ErrNotFound when absent.
	File(ctx context.Context, owner, repo, ref, path string) ([]byte, error)
	// ReleaseAsset downloads a release asset by id (at most MaxAssetBytes).
	ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error)
	// RenderMarkdown renders GitHub-flavoured markdown to sanitized HTML in
	// the context of ownerRepo (so `#123` and `@user` references resolve;
	// relative links and images are left as-is).
	RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error)
	// RepoStars returns the repository's GitHub stargazer count (the
	// directory's "top plugins" ordering).
	RepoStars(ctx context.Context, owner, repo string) (stars int, err error)
}

// GitHubSource implements Source with the GitHub REST API over net/http.
// It is deliberately dependency-free: five endpoints do not justify a
// client library in goblog's module graph.
type GitHubSource struct {
	base        string
	token       string
	userAgent   string
	client      *http.Client // API calls
	assetClient *http.Client // asset downloads: the timeout has to cover up to MaxAssetBytes
}

// NewGitHubSource returns a Source for api.github.com (baseURL "") or a
// test server. token may be empty for unauthenticated access (60 requests
// per hour instead of 5000).
func NewGitHubSource(token, baseURL string) *GitHubSource {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &GitHubSource{
		base:        strings.TrimSuffix(baseURL, "/"),
		token:       token,
		userAgent:   "goblog-directory",
		client:      &http.Client{Timeout: 30 * time.Second},
		assetClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// SetUserAgent sets the User-Agent sent to GitHub (which requires one).
func (g *GitHubSource) SetUserAgent(ua string) { g.userAgent = ua }

func (g *GitHubSource) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	return req, nil
}

// do runs req and returns the body of a 2xx response, reading at most limit
// bytes. A 404 is ErrNotFound (wrapped by callers with what was looked up);
// an exhausted rate limit is named explicitly so the operator knows the fix.
func (g *GitHubSource) do(client *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, fmt.Errorf("%s: GitHub API rate limit exceeded (set the directory's github_token setting to raise it)", req.URL.Path)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("%s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", req.URL.Path, limit)
	}
	return b, nil
}

func (g *GitHubSource) getJSON(ctx context.Context, path string, v any) error {
	req, err := g.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	b, err := g.do(g.client, req, maxAPIBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type apiRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"` // null for drafts
	Assets      []struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		Size               int    `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (g *GitHubSource) Releases(ctx context.Context, owner, repo string) ([]Release, error) {
	var out []Release
	for page := 1; ; page++ {
		var rels []apiRelease
		path := fmt.Sprintf("/repos/%s/%s/releases?per_page=100&page=%d", owner, repo, page)
		if err := g.getJSON(ctx, path, &rels); err != nil {
			return nil, fmt.Errorf("list releases for %s/%s: %w", owner, repo, err)
		}
		for _, r := range rels {
			rel := Release{Tag: r.TagName, Name: r.Name, Body: r.Body, URL: r.HTMLURL, Draft: r.Draft, Prerelease: r.Prerelease}
			if r.PublishedAt != nil {
				rel.PublishedAt = *r.PublishedAt
			}
			for _, a := range r.Assets {
				rel.Assets = append(rel.Assets, Asset{ID: a.ID, Name: a.Name, Size: a.Size, DownloadURL: a.BrowserDownloadURL})
			}
			out = append(out, rel)
		}
		if len(rels) < 100 {
			return out, nil
		}
	}
}

func (g *GitHubSource) File(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	var fc struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref), &fc)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%s/%s@%s:%s: %w", owner, repo, ref, path, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	if fc.Type != "file" {
		return nil, fmt.Errorf("%s/%s@%s:%s is not a file", owner, repo, ref, path)
	}
	if fc.Encoding != "base64" {
		return nil, fmt.Errorf("decode %s/%s@%s:%s: unexpected encoding %q", owner, repo, ref, path, fc.Encoding)
	}
	// GitHub wraps the base64 body at 60 columns.
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(fc.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	return b, nil
}

func (g *GitHubSource) ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error) {
	req, err := g.newRequest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/releases/assets/%d", owner, repo, assetID), nil)
	if err != nil {
		return nil, err
	}
	// With this Accept the API answers with the bytes (via a redirect to
	// the storage host, which the client follows).
	req.Header.Set("Accept", "application/octet-stream")
	b, err := g.do(g.assetClient, req, MaxAssetBytes)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds") {
			return nil, fmt.Errorf("asset %d of %s/%s exceeds %d bytes", assetID, owner, repo, MaxAssetBytes)
		}
		return nil, fmt.Errorf("download asset %d of %s/%s: %w", assetID, owner, repo, err)
	}
	return b, nil
}

func (g *GitHubSource) RepoStars(ctx context.Context, owner, repo string) (int, error) {
	var r struct {
		Stargazers int `json:"stargazers_count"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo), &r); err != nil {
		return 0, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}
	return r.Stargazers, nil
}

func (g *GitHubSource) RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error) {
	if markdown == "" {
		return "", nil
	}
	body, _ := json.Marshal(map[string]string{"text": markdown, "mode": "gfm", "context": ownerRepo})
	req, err := g.newRequest(ctx, http.MethodPost, "/markdown", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	b, err := g.do(g.client, req, maxAPIBytes)
	if err != nil {
		return "", fmt.Errorf("render markdown for %s: %w", ownerRepo, err)
	}
	return string(b), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./plugins/directory/registry/ -run TestGitHubSource -v`
Expected: PASS ×3. (`fakeGitHub`'s asset 12 streams `MaxAssetBytes+1` zero bytes; the `LimitReader(limit+1)` in `do` reads exactly that and refuses — no full read into memory beyond the cap.)

- [ ] **Step 5: Commit**

```bash
git add plugins/directory/registry && git commit -m "registry: GitHub source on net/http

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `registry` package — in-process wasm validator

**Files:**
- Create: `plugins/directory/registry/validator.go`, `plugins/directory/registry/validator_test.go`

**Interfaces:**
- Consumes: `wasm.LoadBytes(data []byte, opts wasm.Options) (*wasm.Plugin, error)`, `(*wasm.Plugin).Identity()`, `(*wasm.Plugin).Close()` from `goblog/plugin/wasm`; `plugin.Info{Name, DisplayName, Version, Runtime}` from `goblog/plugin`.
- Produces:
  ```go
  type Validator interface{ Validate(ctx context.Context, module []byte) (plugin.Info, error) }
  type WasmValidator struct{}
  type FakeValidator struct{ Infos map[string]plugin.Info; Err error }   // keyed by Sum(module)
  func Sum(b []byte) string   // hex sha256
  ```

- [ ] **Step 1: Write the tests**

```go
package registry

import (
	"context"
	"errors"
	"os"
	"testing"

	"goblog/plugin"
)

func echoModule(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../plugin/wasm/testdata/echo.wasm")
	if err != nil {
		t.Fatalf("fixture missing (run go generate ./plugin/wasm): %v", err)
	}
	return b
}

func TestWasmValidator_ReportsIdentity(t *testing.T) {
	info, err := WasmValidator{}.Validate(context.Background(), echoModule(t))
	if err != nil {
		t.Fatal(err)
	}
	want := plugin.Info{Name: "echo", DisplayName: "Echo", Version: "1.2.3", Runtime: "wasm"}
	if info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
}

func TestWasmValidator_RejectsGarbage(t *testing.T) {
	if _, err := (WasmValidator{}).Validate(context.Background(), []byte("not wasm")); err == nil {
		t.Error("garbage bytes must not validate")
	}
}

func TestWasmValidator_HonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (WasmValidator{}).Validate(ctx, echoModule(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestFakeValidator(t *testing.T) {
	m := []byte("\x00asm x")
	f := &FakeValidator{Infos: map[string]plugin.Info{Sum(m): {Name: "x", Version: "1.0.0", Runtime: "wasm"}}}
	if info, err := f.Validate(context.Background(), m); err != nil || info.Name != "x" {
		t.Errorf("known module: %+v %v", info, err)
	}
	if _, err := f.Validate(context.Background(), []byte("other")); err == nil {
		t.Error("unknown module must fail")
	}
	f.Err = errors.New("boom")
	if _, err := f.Validate(context.Background(), m); err == nil {
		t.Error("Err must be returned")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/registry/ -run 'Validator' `
Expected: FAIL — `undefined: WasmValidator`.

- [ ] **Step 3: Write `validator.go`**

```go
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"goblog/plugin"
	"goblog/plugin/wasm"
)

// Validator loads a plugin module the way goblog would and reports its
// identity. The real one instantiates the module in-process; tests use
// FakeValidator.
type Validator interface {
	Validate(ctx context.Context, module []byte) (plugin.Info, error)
}

// WasmValidator instantiates the module with goblog's own runtime — no
// store, no allowed hosts, the runtime's memory cap and call timeouts — and
// reads its identity export. That is the same sandbox an installed plugin
// runs in, so validating a submission is no more exposure than a site
// installing it.
type WasmValidator struct{}

// loadMu serializes instantiation: a burst of public submissions must not
// stack several 64 MB instances at once.
var loadMu sync.Mutex

func (WasmValidator) Validate(ctx context.Context, module []byte) (plugin.Info, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return plugin.Info{}, err
	}
	p, err := wasm.LoadBytes(module, wasm.Options{Logf: func(string, ...any) {}})
	if err != nil {
		return plugin.Info{}, err
	}
	defer p.Close()
	id := p.Identity()
	if id.Name == "" || id.Version == "" {
		return plugin.Info{}, errors.New("identity returned an empty name or version")
	}
	return plugin.Info{Name: id.Name, DisplayName: id.DisplayName, Version: id.Version, Runtime: "wasm"}, nil
}

// Sum is the hex sha256 of b — the index's sha256 field and the key
// FakeValidator answers by.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FakeValidator answers by the sha256 of the module bytes it is given. It is
// exported so the directory package's tests can use it.
type FakeValidator struct {
	Infos map[string]plugin.Info
	Err   error
}

func (f *FakeValidator) Validate(_ context.Context, module []byte) (plugin.Info, error) {
	if f.Err != nil {
		return plugin.Info{}, f.Err
	}
	if info, ok := f.Infos[Sum(module)]; ok {
		return info, nil
	}
	return plugin.Info{}, errors.New("fake: does not load")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./plugins/directory/registry/ -run 'Validator' -v`
Expected: PASS ×4. If `wasm.Options` has no `Logf` field in this checkout, use `wasm.Options{}`.

- [ ] **Step 5: Commit**

```bash
git add plugins/directory/registry && git commit -m "registry: in-process wasm validator

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `registry` package — `ValidateEntry`, `BuildRepo`, `LatestVersion`

**Files:**
- Create: `plugins/directory/registry/validate.go`, `validate_test.go`, `build.go`, `build_test.go`
- Source to port: `~/dev/goblog-plugins/internal/registry/validate.go`, `validate_test.go`, `build.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  ```go
  var TagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
  type Validated struct{ Repo, Owner, Name string; Manifest Manifest; Release Release; Version string; Releases []Release; Asset Asset; Entry []byte; SHA256 string }
  func ValidateEntry(ctx, src Source, val Validator, repo string) (*Validated, error)
  func LatestVersion(ctx, src Source, repo string) (string, error)
  type IndexEntry struct{ Name, DisplayName, Description, Version, Author, License, SourceURL, DownloadURL, SHA256, MinGoblogVersion, InstallType, Runtime string; AllowedHosts []string; ReleasedAt, DetailURL string; Stars int }  // json tags as in Global Constraints
  type ReleaseDoc struct{ Version, ReleasedAt string; NotesHTML template.HTML; URL string }
  type DetailDoc struct{ IndexEntry; ReadmeHTML, ChangelogHTML template.HTML; Releases []ReleaseDoc }
  func BuildRepo(ctx, src Source, val Validator, repo, baseURL string) (DetailDoc, error)
  ```

- [ ] **Step 1: Copy `validate_test.go` and adapt**

```bash
cp ~/dev/goblog-plugins/internal/registry/validate_test.go plugins/directory/registry/validate_test.go
```

Edits: add `"goblog/plugin"` to the imports; replace every `Info{` with `plugin.Info{` and `map[string]Info` with `map[string]plugin.Info`; replace `sum(` with `Sum(`. Append:

```go
func TestLatestVersion(t *testing.T) {
	v, err := LatestVersion(context.Background(), helloSource(), "o/hello")
	if err != nil || v != "1.1.0" {
		t.Errorf("LatestVersion = %q, %v", v, err)
	}
	src := helloSource()
	src.releases["o/hello"] = nil
	if _, err := LatestVersion(context.Background(), src, "o/hello"); err == nil {
		t.Error("no releases must be an error")
	}
}
```

- [ ] **Step 2: Copy `validate.go` and adapt**

```bash
cp ~/dev/goblog-plugins/internal/registry/validate.go plugins/directory/registry/validate.go
```

Factor the release selection out so `LatestVersion` shares it. Replace the block in `ValidateEntry` from `all, err := src.Releases(...)` through `releases = filtered` with:

```go
	latest, releases, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(latest.Tag, "v")
```

and add:

```go
// latestRelease picks the newest published, non-prerelease release (by
// date, not GitHub's order) and the vX.Y.Z-tagged history shown to readers.
// The tag check runs on the newest release before filtering, so a bad tag
// on the latest release is an error rather than silently skipped.
func latestRelease(ctx context.Context, src Source, repo, owner, name string) (Release, []Release, error) {
	all, err := src.Releases(ctx, owner, name)
	if err != nil {
		return Release{}, nil, err
	}
	var releases []Release
	for _, r := range all {
		if !r.Draft && !r.Prerelease {
			releases = append(releases, r)
		}
	}
	if len(releases) == 0 {
		return Release{}, nil, fmt.Errorf("%s: no published release (drafts and pre-releases are ignored)", repo)
	}
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].PublishedAt.After(releases[j].PublishedAt) })
	latest := releases[0]
	if !TagPattern.MatchString(latest.Tag) {
		return Release{}, nil, fmt.Errorf("%s: release tag %q must be vX.Y.Z", repo, latest.Tag)
	}
	filtered := releases[:0:0]
	for _, r := range releases {
		if TagPattern.MatchString(r.Tag) {
			filtered = append(filtered, r)
		}
	}
	return latest, filtered, nil
}

// LatestVersion is the version ValidateEntry would publish for repo, found
// without downloading anything. The scheduled rebuild uses it to skip repos
// whose latest release is already built.
func LatestVersion(ctx context.Context, src Source, repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("%q: repo must be owner/name", repo)
	}
	latest, _, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(latest.Tag, "v"), nil
}
```

Keep the rest of `ValidateEntry` (manifest, README, asset, validator, identity checks, sha256) exactly as ported. Where it references `Info`, that is now `plugin.Info` (add the import). Keep `isNotFound`.

- [ ] **Step 3: Run the validate tests**

Run: `go test ./plugins/directory/registry/ -run 'ValidateEntry|LatestVersion' -v`
Expected: FAIL only on `build`-related symbols if any; otherwise PASS. (`validate_test.go` references `goodManifest` from Task 1 and `FakeValidator`/`Sum` from Task 3.)

- [ ] **Step 4: Write `build_test.go`**

```go
package registry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRepo_Good(t *testing.T) {
	src := helloSource()
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	want := IndexEntry{
		Name: "hello", DisplayName: "Hello", Description: "Says hi.", Version: "1.1.0", Author: "Jason Ernst",
		License: "Apache-2.0", SourceURL: "https://github.com/o/hello",
		DownloadURL: "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm", SHA256: Sum(helloWasm),
		MinGoblogVersion: "0.2.6", InstallType: "wasm", Runtime: "wasm", AllowedHosts: []string{"api.example.test"},
		ReleasedAt: "2026-09-15T00:00:00Z",
		DetailURL:  "https://example.test/plugins/hello.json", Stars: 7,
	}
	if !reflect.DeepEqual(d.IndexEntry, want) {
		t.Errorf("entry =\n%+v\nwant\n%+v", d.IndexEntry, want)
	}
	if d.ReadmeHTML != "<p># Hello</p>" || d.ChangelogHTML != "<p>## 1.1.0\n- second</p>" {
		t.Errorf("readme/changelog = %q %q", d.ReadmeHTML, d.ChangelogHTML)
	}
	if len(d.Releases) != 2 || d.Releases[0].Version != "1.1.0" || d.Releases[0].NotesHTML != "<p>Second</p>" ||
		d.Releases[1].Version != "1.0.0" || d.Releases[1].URL != "https://github.com/o/hello/releases/tag/v1.0.0" {
		t.Errorf("releases = %+v", d.Releases)
	}
}

func TestBuildRepo_RelativeDetailURLWithoutBase(t *testing.T) {
	d, err := BuildRepo(context.Background(), helloSource(), helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.DetailURL != "/plugins/hello.json" {
		t.Errorf("detail_url = %q", d.DetailURL)
	}
}

func TestBuildRepo_NoChangelogAndEmptyHostsSerialiseCleanly(t *testing.T) {
	src := helloSource()
	delete(src.files, "o/hello@v1.1.0:CHANGELOG.md")
	src.files["o/hello@v1.1.0:goblog-plugin.json"] = strings.Replace(goodManifest, `"allowed_hosts": ["api.example.test"],`, "", 1)
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.ChangelogHTML != "" || d.AllowedHosts == nil || len(d.AllowedHosts) != 0 {
		t.Errorf("doc = %+v", d)
	}
}

func TestBuildRepo_StarsAreBestEffort(t *testing.T) {
	src := helloSource()
	src.starsErr = errors.New("github down")
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Stars != 0 {
		t.Errorf("stars should fall back to 0, got %d", d.Stars)
	}
}

func TestBuildRepo_ValidationErrorsPropagate(t *testing.T) {
	src := helloSource()
	delete(src.files, "o/hello@v1.1.0:README.md")
	if _, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", ""); err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Errorf("want README error, got %v", err)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `go test ./plugins/directory/registry/ -run TestBuildRepo`
Expected: FAIL — `undefined: BuildRepo`.

- [ ] **Step 6: Write `build.go`**

```go
package registry

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"
)

// IndexEntry is one element of index.json: the latest release of a plugin.
// Field names are the contract consumed by goblog's directory plugin and
// the admin installer; do not rename them.
type IndexEntry struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description"`
	Version          string   `json:"version"`
	Author           string   `json:"author"`
	License          string   `json:"license"`
	SourceURL        string   `json:"source_url"`
	DownloadURL      string   `json:"download_url"`
	SHA256           string   `json:"sha256"`
	MinGoblogVersion string   `json:"min_goblog_version"`
	InstallType      string   `json:"install_type"`  // "wasm"
	Runtime          string   `json:"runtime"`       // "wasm"
	AllowedHosts     []string `json:"allowed_hosts"` // never null: [] when the plugin uses no network
	ReleasedAt       string   `json:"released_at"`   // RFC 3339
	DetailURL        string   `json:"detail_url"`
	Stars            int      `json:"stars"`
}

// ReleaseDoc is one release in a plugin's history.
type ReleaseDoc struct {
	Version    string        `json:"version"`
	ReleasedAt string        `json:"released_at"`
	NotesHTML  template.HTML `json:"notes_html"` // rendered and sanitized by GitHub's markdown API
	URL        string        `json:"url"`
}

// DetailDoc is /plugins/<name>.json: the index entry plus rendered README,
// changelog and release history. The *_html fields come from GitHub's
// markdown API, which sanitizes them; they are the only fields pages insert
// unescaped.
type DetailDoc struct {
	IndexEntry
	ReadmeHTML    template.HTML `json:"readme_html"`
	ChangelogHTML template.HTML `json:"changelog_html"`
	Releases      []ReleaseDoc  `json:"releases"`
}

// BuildRepo validates repo end to end and returns the document the
// directory publishes for it. baseURL is the site's URL ("" makes
// detail_url relative).
func BuildRepo(ctx context.Context, src Source, val Validator, repo, baseURL string) (DetailDoc, error) {
	v, err := ValidateEntry(ctx, src, val, repo)
	if err != nil {
		return DetailDoc{}, err
	}
	return buildDetail(ctx, src, v, baseURL)
}

func buildDetail(ctx context.Context, src Source, v *Validated, baseURL string) (DetailDoc, error) {
	ownerRepo := v.Owner + "/" + v.Name
	entry := IndexEntry{
		Name:             v.Manifest.Name,
		DisplayName:      v.Manifest.DisplayName,
		Description:      v.Manifest.Description,
		Version:          v.Version,
		Author:           v.Manifest.Author,
		License:          v.Manifest.License,
		SourceURL:        "https://github.com/" + ownerRepo,
		DownloadURL:      v.Asset.DownloadURL,
		SHA256:           v.SHA256,
		MinGoblogVersion: v.Manifest.MinGoblogVersion,
		InstallType:      "wasm",
		Runtime:          "wasm",
		AllowedHosts:     v.Manifest.AllowedHosts,
		ReleasedAt:       v.Release.PublishedAt.UTC().Format(time.RFC3339),
		DetailURL:        strings.TrimSuffix(baseURL, "/") + "/plugins/" + v.Manifest.Name + ".json",
	}

	// Stars only order the directory; a failed lookup must not drop an
	// otherwise valid plugin.
	if stars, err := src.RepoStars(ctx, v.Owner, v.Name); err != nil {
		log.Printf("%s: stars unavailable, using 0: %v", ownerRepo, err)
	} else {
		entry.Stars = stars
	}

	readme, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "README.md")
	if err != nil {
		return DetailDoc{}, fmt.Errorf("README.md: %w", err)
	}
	readmeHTML, err := src.RenderMarkdown(ctx, ownerRepo, string(readme))
	if err != nil {
		return DetailDoc{}, err
	}
	changelogHTML := ""
	if cl, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "CHANGELOG.md"); err == nil {
		if changelogHTML, err = src.RenderMarkdown(ctx, ownerRepo, string(cl)); err != nil {
			return DetailDoc{}, err
		}
	} else if !isNotFound(err) {
		return DetailDoc{}, fmt.Errorf("CHANGELOG.md: %w", err)
	}

	releases := make([]ReleaseDoc, 0, len(v.Releases))
	for _, r := range v.Releases {
		notes, err := src.RenderMarkdown(ctx, ownerRepo, r.Body)
		if err != nil {
			return DetailDoc{}, err
		}
		releases = append(releases, ReleaseDoc{
			Version:    strings.TrimPrefix(r.Tag, "v"),
			ReleasedAt: r.PublishedAt.UTC().Format(time.RFC3339),
			NotesHTML:  template.HTML(notes),
			URL:        r.URL,
		})
	}
	return DetailDoc{IndexEntry: entry, ReadmeHTML: template.HTML(readmeHTML), ChangelogHTML: template.HTML(changelogHTML), Releases: releases}, nil
}
```

- [ ] **Step 7: Run the whole package**

Run: `go test ./plugins/directory/registry/ -v && go vet ./plugins/directory/registry/`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add plugins/directory/registry && git commit -m "registry: ValidateEntry, BuildRepo and LatestVersion

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Directory store — models, migration, index generation

**Files:**
- Create: `plugins/directory/store.go`, `plugins/directory/store_test.go`
- Modify: `plugins/directory/index.go` (aliases)

**Interfaces:**
- Consumes: `registry.IndexEntry`, `registry.DetailDoc`.
- Produces:
  ```go
  const (StatusPending = "pending"; StatusApproved = "approved"; StatusRejected = "rejected")
  type Repo struct{ ID uint; Repo string; Status string; SubmittedAt time.Time; DecidedAt *time.Time; RejectReason string; SubmitterIP string }
  type Build struct{ ID uint; RepoID uint; Name, Version string; Stars int; Doc string; BuiltAt time.Time; LastAttemptAt *time.Time; LastError string }
  func Migrate(db *gorm.DB) error
  func (b *Build) Detail() (registry.DetailDoc, error)
  func (b *Build) SetDoc(d registry.DetailDoc) error      // sets Name, Version, Stars, Doc, BuiltAt, clears LastError
  func approvedDocs(db *gorm.DB) ([]registry.DetailDoc, error)   // sorted by name
  func encodeIndex(docs []registry.DetailDoc) ([]byte, []registry.IndexEntry)
  type Entry = registry.IndexEntry; type Detail = registry.DetailDoc; type Release = registry.ReleaseDoc
  ```

- [ ] **Step 1: Replace `index.go` with aliases**

```go
package directory

import "goblog/plugins/directory/registry"

// Entry, Detail and Release are the wire types of index.json and
// /plugins/<name>.json. The registry package builds them; the installer
// (in every goblog) decodes them. They are aliases so both sides share one
// definition.
type (
	Entry   = registry.IndexEntry
	Detail  = registry.DetailDoc
	Release = registry.ReleaseDoc
)
```

Run: `go build ./...` — expected to still compile (the fetcher and templates use the same field names; `Release.NotesHTML` etc. are already `template.HTML`).

- [ ] **Step 2: Write `store_test.go`**

```go
package directory

import (
	"encoding/json"
	"testing"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func doc(name, version string, stars int) registry.DetailDoc {
	return registry.DetailDoc{
		IndexEntry: registry.IndexEntry{Name: name, DisplayName: name, Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: "https://github.com/o/" + name + "/releases/download/v" + version + "/plugin.wasm",
			SHA256: "00", MinGoblogVersion: "0.3.0", InstallType: "wasm", Runtime: "wasm", AllowedHosts: []string{}, ReleasedAt: "2026-09-15T00:00:00Z",
			DetailURL: "/plugins/" + name + ".json", Stars: stars},
		ReadmeHTML: "<p>readme</p>", Releases: []registry.ReleaseDoc{},
	}
}

// seed inserts a repo with a build in the given status and returns it.
func seed(t *testing.T, db *gorm.DB, repo, status string, d registry.DetailDoc) Repo {
	t.Helper()
	r := Repo{Repo: repo, Status: status, SubmittedAt: time.Now()}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	b := Build{RepoID: r.ID}
	if err := b.SetDoc(d); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMigrate_IsIdempotentAndEnforcesUniqueness(t *testing.T) {
	db := testDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seed(t, db, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	if err := db.Create(&Repo{Repo: "o/hello", Status: StatusPending, SubmittedAt: time.Now()}).Error; err == nil {
		t.Error("repo must be unique")
	}
	other := Repo{Repo: "o/other", Status: StatusPending, SubmittedAt: time.Now()}
	db.Create(&other)
	b := Build{RepoID: other.ID}
	b.SetDoc(doc("hello", "2.0.0", 0))
	if err := db.Create(&b).Error; err == nil {
		t.Error("build name must be unique")
	}
}

func TestBuild_DocRoundTrip(t *testing.T) {
	var b Build
	want := doc("hello", "1.2.3", 9)
	if err := b.SetDoc(want); err != nil {
		t.Fatal(err)
	}
	if b.Name != "hello" || b.Version != "1.2.3" || b.Stars != 9 || b.BuiltAt.IsZero() || b.LastError != "" {
		t.Errorf("build = %+v", b)
	}
	got, err := b.Detail()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "hello" || got.ReadmeHTML != "<p>readme</p>" || got.AllowedHosts == nil {
		t.Errorf("detail = %+v", got)
	}
}

func TestApprovedDocsAndEncodeIndex(t *testing.T) {
	db := testDB(t)
	seed(t, db, "o/zeta", StatusApproved, doc("zeta", "0.1.0", 3))
	seed(t, db, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	seed(t, db, "o/pending", StatusPending, doc("pending", "1.0.0", 99))
	seed(t, db, "o/rejected", StatusRejected, doc("rejected", "1.0.0", 99))

	docs, err := approvedDocs(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Name != "hello" || docs[1].Name != "zeta" {
		t.Fatalf("approved docs should be the approved ones sorted by name: %+v", docs)
	}
	raw, entries := encodeIndex(docs)
	var decoded []registry.IndexEntry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0].Name != "hello" || decoded[0].AllowedHosts == nil || len(entries) != 2 {
		t.Errorf("index = %s", raw)
	}
	if raw, _ := encodeIndex(nil); string(raw) != "[]\n" {
		t.Errorf("empty index must be [], got %q", raw)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./plugins/directory/ -run 'Migrate|Build_Doc|ApprovedDocs'`
Expected: FAIL — `undefined: Migrate`.

- [ ] **Step 4: Write `store.go`**

```go
package directory

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// Submission states of a repository in the directory.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Repo is one GitHub repository that was ever submitted to this directory:
// the curation record. Its build (what gets published) is a separate row
// because the two change on different schedules.
type Repo struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Repo         string     `gorm:"uniqueIndex;size:255" json:"repo"` // owner/name, lower-case
	Status       string     `gorm:"index;size:16" json:"status"`
	SubmittedAt  time.Time  `json:"submitted_at"`
	DecidedAt    *time.Time `json:"decided_at"`
	RejectReason string     `json:"reject_reason"`
	// SubmitterIP is kept for the admin's benefit and blanked by the
	// scheduled job after 7 days.
	SubmitterIP string `gorm:"size:64" json:"-"`
}

func (Repo) TableName() string { return "directory_repos" }

// Build is the last successful build of a repository: the detail document
// as JSON plus the columns the directory sorts and routes on. A failed
// rebuild leaves Doc alone and records LastError, so one broken release
// never takes a plugin off the directory.
type Build struct {
	ID            uint       `gorm:"primaryKey"`
	RepoID        uint       `gorm:"uniqueIndex"`
	Name          string     `gorm:"uniqueIndex;size:128"` // plugin name; routes /plugins/<name>
	Version       string     `gorm:"size:32"`
	Stars         int
	Doc           string `gorm:"type:text"` // registry.DetailDoc as JSON
	BuiltAt       time.Time
	LastAttemptAt *time.Time
	LastError     string `gorm:"type:text"`
}

func (Build) TableName() string { return "directory_builds" }

// Migrate creates or updates the directory's tables.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&Repo{}, &Build{})
}

// Detail decodes the stored document.
func (b *Build) Detail() (registry.DetailDoc, error) {
	var d registry.DetailDoc
	err := json.Unmarshal([]byte(b.Doc), &d)
	if d.AllowedHosts == nil {
		d.AllowedHosts = []string{}
	}
	return d, err
}

// SetDoc stores d as this build: the indexed columns, the JSON, the build
// time, and a cleared error.
func (b *Build) SetDoc(d registry.DetailDoc) error {
	raw, err := marshalJSON(d)
	if err != nil {
		return err
	}
	b.Name, b.Version, b.Stars, b.Doc = d.Name, d.Version, d.Stars, string(raw)
	b.BuiltAt = time.Now()
	b.LastError = ""
	return nil
}

// approvedDocs returns the documents of every approved repository, sorted
// by plugin name — the content of index.json.
func approvedDocs(db *gorm.DB) ([]registry.DetailDoc, error) {
	var builds []Build
	err := db.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
		Where("directory_repos.status = ?", StatusApproved).Find(&builds).Error
	if err != nil {
		return nil, err
	}
	docs := make([]registry.DetailDoc, 0, len(builds))
	for _, b := range builds {
		d, err := b.Detail()
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

// encodeIndex renders index.json ([] when empty) and the entries it holds.
func encodeIndex(docs []registry.DetailDoc) ([]byte, []registry.IndexEntry) {
	entries := make([]registry.IndexEntry, 0, len(docs))
	for _, d := range docs {
		entries = append(entries, d.IndexEntry)
	}
	raw, _ := marshalJSON(entries)
	return raw, entries
}

// marshalJSON encodes without HTML escaping (the docs carry HTML) and with
// the indentation the old GitHub Pages index had.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./plugins/directory/ -run 'Migrate|Build_Doc|ApprovedDocs' -v`
Expected: PASS ×3.

- [ ] **Step 6: Commit**

```bash
git add plugins/directory && git commit -m "directory: repo and build tables

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Per-IP submission limiter and `ParseRepo`

**Files:**
- Create: `plugins/directory/limiter.go`, `plugins/directory/limiter_test.go`, `plugins/directory/submit.go` (ParseRepo only for now), `plugins/directory/submit_test.go`

**Interfaces:**
- Produces:
  ```go
  type ipLimiter struct{...}
  func newIPLimiter(limit int, window time.Duration) *ipLimiter
  func (l *ipLimiter) Allow(ip string) bool
  var ErrBadRepo = errors.New("enter a GitHub repository URL like https://github.com/owner/repo")
  func ParseRepo(input string) (string, error)   // "owner/repo", lower-case
  ```

- [ ] **Step 1: Write the tests**

`limiter_test.go`:

```go
package directory

import (
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l := newIPLimiter(2, time.Hour)
	l.now = func() time.Time { return now }
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("first two attempts must pass")
	}
	if l.Allow("a") {
		t.Error("third attempt within the window must be refused")
	}
	if !l.Allow("b") {
		t.Error("another address is counted separately")
	}
	now = now.Add(61 * time.Minute)
	if !l.Allow("a") {
		t.Error("attempts outside the window no longer count")
	}
}
```

`submit_test.go`:

```go
package directory

import (
	"errors"
	"testing"
)

func TestParseRepo(t *testing.T) {
	good := map[string]string{
		"https://github.com/Owner/Repo":            "owner/repo",
		"http://www.github.com/o/r/":               "o/r",
		"https://github.com/o/r.git":               "o/r",
		"https://github.com/o/r/releases/tag/v1":   "o/r",
		"  github.com/o/goblog-plugin-x ":          "o/goblog-plugin-x",
		"o/r":                                      "o/r",
		"o/r.js":                                   "o/r.js",
	}
	for in, want := range good {
		if got, err := ParseRepo(in); err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "o", "o/", "/r", "o/r/x", "https://gitlab.com/o/r", "https://github.com/o", "o/..", "javascript:alert(1)"} {
		if _, err := ParseRepo(in); !errors.Is(err, ErrBadRepo) {
			t.Errorf("ParseRepo(%q) should be ErrBadRepo, got %v", in, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/ -run 'IPLimiter|ParseRepo'`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write `limiter.go`**

```go
package directory

import (
	"sync"
	"time"
)

// ipLimiter allows at most limit attempts per address per window. It is
// in-memory on purpose: it only has to blunt a burst of public submissions
// (each of which costs GitHub API calls and a module instantiation), and
// resetting on restart is fine.
type ipLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// Allow records an attempt from ip and reports whether it is within the
// limit. Refused attempts are not recorded, so a blocked client is not
// pushed further out by retrying.
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}
```

- [ ] **Step 4: Write `submit.go` with `ParseRepo`**

```go
package directory

import (
	"errors"
	"regexp"
	"strings"
)

// ErrBadRepo is the message shown when the submitted text is not a GitHub
// repository.
var ErrBadRepo = errors.New("enter a GitHub repository URL like https://github.com/owner/repo")

// repoURLPattern accepts a github.com URL (with or without scheme, www,
// .git or a trailing path) and captures owner and repository.
var repoURLPattern = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(?:/.*)?$`)

// repoShortPattern accepts bare owner/repo.
var repoShortPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// ParseRepo turns what a person typed into the canonical "owner/repo" key
// (lower-case, so the same repository cannot be submitted twice under
// different capitalisation).
func ParseRepo(input string) (string, error) {
	in := strings.TrimSpace(input)
	m := repoURLPattern.FindStringSubmatch(in)
	if m == nil {
		m = repoShortPattern.FindStringSubmatch(in)
	}
	if m == nil {
		return "", ErrBadRepo
	}
	owner, name := m[1], strings.TrimSuffix(m[2], ".git")
	if name == "" || name == "." || name == ".." || owner == "." || owner == ".." {
		return "", ErrBadRepo
	}
	return strings.ToLower(owner + "/" + name), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./plugins/directory/ -run 'IPLimiter|ParseRepo' -v`
Expected: PASS ×2.

- [ ] **Step 6: Commit**

```bash
git add plugins/directory && git commit -m "directory: submission rate limiter and repo URL parsing

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Directory `Service` — submit, curate, rebuild, index cache

**Files:**
- Create: `plugins/directory/service.go`, `plugins/directory/service_test.go`

**Interfaces:**
- Consumes: Tasks 4–6 (`registry.BuildRepo`, `registry.LatestVersion`, `registry.Source`, `registry.Validator`, `Repo`, `Build`, `approvedDocs`, `encodeIndex`, `ipLimiter`, `ParseRepo`).
- Produces:
  ```go
  var ErrAlreadyListed, ErrUnderReview, ErrNameTaken, ErrRateLimited, ErrBusy, ErrNotFound error
  type ValidationError struct{ Msg string }        // Error() = Msg
  type RepoView struct{...}                          // JSON for the admin list
  func NewService(db *gorm.DB, newSource func(token string) registry.Source, val registry.Validator, siteURL func() string) *Service
  func (s *Service) Submit(ctx context.Context, input, ip, token string) (*Repo, error)
  func (s *Service) Add(ctx context.Context, input, token string) (*Repo, error)
  func (s *Service) Approve(id uint) error
  func (s *Service) Reject(id uint, reason string) error
  func (s *Service) Delist(id uint) error
  func (s *Service) Rebuild(ctx context.Context, id uint, token string) error
  func (s *Service) RefreshAll(ctx context.Context, token string) error
  func (s *Service) LastRefresh() time.Time
  func (s *Service) List(status string) ([]RepoView, error)
  func (s *Service) Get(id uint) (RepoView, registry.DetailDoc, error)
  func (s *Service) Index() (raw []byte, entries []registry.IndexEntry)
  func (s *Service) Detail(name string) (registry.DetailDoc, bool)
  ```

- [ ] **Step 1: Write the test fixture and tests**

`service_test.go`:

```go
package directory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goblog/plugin"
	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// fakeRepo is one GitHub repository as the fake source presents it: a
// single vX.Y.Z release with plugin.wasm attached.
type fakeRepo struct {
	version string
	name    string // manifest + identity name
	stars   int
	readme  string
	wasm    []byte
}

type fakeSource struct {
	repos    map[string]*fakeRepo
	assets   atomic.Int32 // ReleaseAsset calls
	rendered atomic.Int32 // RenderMarkdown calls
}

func (f *fakeSource) repo(owner, repo string) (*fakeRepo, error) {
	r, ok := f.repos[owner+"/"+repo]
	if !ok {
		return nil, fmt.Errorf("%s/%s: no such repository", owner, repo)
	}
	return r, nil
}

func (f *fakeSource) Releases(_ context.Context, owner, repo string) ([]registry.Release, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	return []registry.Release{{
		Tag: "v" + r.version, Body: "notes", URL: "https://github.com/" + owner + "/" + repo + "/releases/tag/v" + r.version,
		PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		Assets: []registry.Asset{{ID: 1, Name: "plugin.wasm", Size: len(r.wasm),
			DownloadURL: "https://github.com/" + owner + "/" + repo + "/releases/download/v" + r.version + "/plugin.wasm"}},
	}}, nil
}

func (f *fakeSource) File(_ context.Context, owner, repo, ref, path string) ([]byte, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	switch path {
	case "goblog-plugin.json":
		return []byte(`{"name":"` + r.name + `","display_name":"` + strings.ToUpper(r.name) + `","description":"Says hi.","author":"Jason","license":"MIT","runtime":"wasm","min_goblog_version":"0.3.0"}`), nil
	case "README.md":
		return []byte(r.readme), nil
	}
	return nil, registry.ErrNotFound
}

func (f *fakeSource) ReleaseAsset(_ context.Context, owner, repo string, _ int64) ([]byte, error) {
	f.assets.Add(1)
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	return r.wasm, nil
}

func (f *fakeSource) RenderMarkdown(_ context.Context, _, md string) (string, error) {
	if md == "" {
		return "", nil
	}
	f.rendered.Add(1)
	return "<p>" + md + "</p>", nil
}

func (f *fakeSource) RepoStars(_ context.Context, owner, repo string) (int, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return 0, err
	}
	return r.stars, nil
}

type fixture struct {
	db  *gorm.DB
	src *fakeSource
	val *registry.FakeValidator
	svc *Service
}

// newFixture returns a Service over sqlite with o/hello (v1.0.0, name
// "hello") and o/zeta (v0.1.0, name "zeta") available at the fake GitHub.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	hello := &fakeRepo{version: "1.0.0", name: "hello", stars: 7, readme: "# Hello", wasm: []byte("\x00asm hello")}
	zeta := &fakeRepo{version: "0.1.0", name: "zeta", stars: 1, readme: "# Zeta", wasm: []byte("\x00asm zeta")}
	src := &fakeSource{repos: map[string]*fakeRepo{"o/hello": hello, "o/zeta": zeta}}
	val := &registry.FakeValidator{Infos: map[string]plugin.Info{
		registry.Sum(hello.wasm): {Name: "hello", DisplayName: "Hello", Version: "1.0.0", Runtime: "wasm"},
		registry.Sum(zeta.wasm):  {Name: "zeta", DisplayName: "Zeta", Version: "0.1.0", Runtime: "wasm"},
	}}
	db := testDB(t)
	svc := NewService(db, func(string) registry.Source { return src }, val, func() string { return "https://example.test" })
	return &fixture{db: db, src: src, val: val, svc: svc}
}

func (f *fixture) indexNames(t *testing.T) []string {
	t.Helper()
	_, entries := f.svc.Index()
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

func TestService_SubmitThenApprove(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Submit(context.Background(), "https://github.com/o/hello", "1.1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusPending || r.SubmitterIP != "1.1.1.1" || r.Repo != "o/hello" {
		t.Errorf("repo = %+v", r)
	}
	var b Build
	if err := f.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil || b.Name != "hello" || b.Version != "1.0.0" || b.Stars != 7 {
		t.Errorf("build = %+v %v", b, err)
	}
	if names := f.indexNames(t); len(names) != 0 {
		t.Errorf("pending must not be in the index: %v", names)
	}
	if _, ok := f.svc.Detail("hello"); ok {
		t.Error("pending must not have a public detail")
	}
	if err := f.svc.Approve(r.ID); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t); len(names) != 1 || names[0] != "hello" {
		t.Errorf("approved should be in the index: %v", names)
	}
	d, ok := f.svc.Detail("hello")
	if !ok || d.DetailURL != "https://example.test/plugins/hello.json" || d.ReadmeHTML != "<p># Hello</p>" {
		t.Errorf("detail = %+v %v", d, ok)
	}
	raw, _ := f.svc.Index()
	if !strings.Contains(string(raw), `"name": "hello"`) {
		t.Errorf("raw index = %s", raw)
	}
}

func TestService_SubmitDuplicatesAndResubmit(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.Submit(context.Background(), "o/hello", "ip", "")
	if _, err := f.svc.Submit(context.Background(), "O/Hello", "ip", ""); !errors.Is(err, ErrUnderReview) {
		t.Errorf("pending again: %v", err)
	}
	f.svc.Approve(r.ID)
	if _, err := f.svc.Submit(context.Background(), "o/hello", "ip", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("approved again: %v", err)
	}
	if err := f.svc.Reject(r.ID, "nope"); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t); len(names) != 0 {
		t.Errorf("rejected must leave the index: %v", names)
	}
	f.src.repos["o/hello"].version = "1.1.0"
	f.val.Infos[registry.Sum(f.src.repos["o/hello"].wasm)] = plugin.Info{Name: "hello", Version: "1.1.0", Runtime: "wasm"}
	again, err := f.svc.Submit(context.Background(), "o/hello", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != r.ID || again.Status != StatusPending || again.RejectReason != "" || again.DecidedAt != nil {
		t.Errorf("resubmission should reset the same row: %+v", again)
	}
	var b Build
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.Version != "1.1.0" {
		t.Errorf("resubmission should rebuild: %+v", b)
	}
}

func TestService_SubmitErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, "not a repo", "ip", ""); !errors.Is(err, ErrBadRepo) {
		t.Errorf("bad input: %v", err)
	}
	var ve *ValidationError
	if _, err := f.svc.Submit(ctx, "o/missing", "ip", ""); !errors.As(err, &ve) || !strings.Contains(ve.Msg, "no such repository") {
		t.Errorf("unknown repo should be a ValidationError: %v", err)
	}
	var n int64
	f.db.Model(&Repo{}).Count(&n)
	if n != 0 {
		t.Errorf("failed submissions must not leave rows, got %d", n)
	}

	// Name collision: o/zeta re-using hello's plugin name.
	f.svc.Add(ctx, "o/hello", "")
	f.src.repos["o/zeta"].name = "hello"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "hello", Version: "0.1.0", Runtime: "wasm"}
	if _, err := f.svc.Submit(ctx, "o/zeta", "ip", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
}

func TestService_SubmitRateLimitAndBusy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		f.svc.Submit(ctx, "o/missing", "9.9.9.9", "") // fails validation but counts
	}
	if _, err := f.svc.Submit(ctx, "o/hello", "9.9.9.9", ""); !errors.Is(err, ErrRateLimited) {
		t.Errorf("6th attempt: %v", err)
	}
	if _, err := f.svc.Submit(ctx, "o/hello", "8.8.8.8", ""); err != nil {
		t.Errorf("other address is fine: %v", err)
	}

	f.svc.validate.Lock()
	defer f.svc.validate.Unlock()
	if _, err := f.svc.Submit(ctx, "o/zeta", "7.7.7.7", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("while a validation runs: %v", err)
	}
}

func TestService_AddDelistNotFound(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Add(context.Background(), "https://github.com/o/hello", "")
	if err != nil || r.Status != StatusApproved || r.DecidedAt == nil {
		t.Fatalf("add: %+v %v", r, err)
	}
	if names := f.indexNames(t); len(names) != 1 {
		t.Errorf("index after add: %v", names)
	}
	if _, err := f.svc.Add(context.Background(), "o/hello", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("add twice: %v", err)
	}
	if err := f.svc.Delist(r.ID); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t); len(names) != 0 {
		t.Errorf("index after delist: %v", names)
	}
	var n int64
	f.db.Model(&Build{}).Count(&n)
	if n != 0 {
		t.Error("delist must remove the build")
	}
	for _, err := range []error{f.svc.Approve(999), f.svc.Reject(999, ""), f.svc.Delist(999), f.svc.Rebuild(context.Background(), 999, "")} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id: %v", err)
		}
	}
}

func TestService_RebuildKeepsOldDocOnFailure(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.Add(context.Background(), "o/hello", "")
	f.src.repos["o/hello"].readme = ""
	delete(f.src.repos, "o/hello")
	err := f.svc.Rebuild(context.Background(), r.ID, "")
	if err == nil {
		t.Fatal("rebuild of a vanished repo must fail")
	}
	var b Build
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.Version != "1.0.0" || b.LastError == "" || b.LastAttemptAt == nil {
		t.Errorf("build after failure = %+v", b)
	}
	if names := f.indexNames(t); len(names) != 1 {
		t.Errorf("still listed: %v", names)
	}
	f.src.repos["o/hello"] = &fakeRepo{version: "1.0.0", name: "hello", stars: 8, readme: "# Hello", wasm: []byte("\x00asm hello")}
	if err := f.svc.Rebuild(context.Background(), r.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.LastError != "" || b.Stars != 8 {
		t.Errorf("build after success = %+v", b)
	}
}

func TestService_RefreshAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hello, _ := f.svc.Add(ctx, "o/hello", "")
	f.svc.Add(ctx, "o/zeta", "")
	old := time.Now().Add(-8 * 24 * time.Hour)
	f.db.Model(&Repo{}).Where("id = ?", hello.ID).Updates(map[string]any{"submitter_ip": "1.2.3.4", "submitted_at": old})

	f.src.repos["o/hello"].stars = 70 // unchanged release: stars only
	f.src.repos["o/zeta"].version = "0.2.0"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "zeta", Version: "0.2.0", Runtime: "wasm"}
	assetsBefore := f.src.assets.Load()
	if err := f.svc.RefreshAll(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if f.src.assets.Load()-assetsBefore != 1 {
		t.Errorf("only zeta (new release) should be downloaded, got %d downloads", f.src.assets.Load()-assetsBefore)
	}
	_, entries := f.svc.Index()
	if len(entries) != 2 || entries[0].Name != "hello" || entries[0].Stars != 70 || entries[0].Version != "1.0.0" || entries[1].Version != "0.2.0" {
		t.Errorf("index after refresh = %+v", entries)
	}
	var r Repo
	f.db.First(&r, hello.ID)
	if r.SubmitterIP != "" {
		t.Error("addresses older than 7 days must be blanked")
	}
	if f.svc.LastRefresh().IsZero() {
		t.Error("LastRefresh must be stamped")
	}

	// A failing repo does not stop the others and is reported.
	delete(f.src.repos, "o/hello")
	f.src.repos["o/zeta"].stars = 5
	err := f.svc.RefreshAll(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "o/hello") {
		t.Errorf("want an error naming o/hello, got %v", err)
	}
	_, entries = f.svc.Index()
	if len(entries) != 2 || entries[1].Stars != 5 {
		t.Errorf("zeta should still refresh: %+v", entries)
	}
}

func TestService_ListAndGet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.Add(ctx, "o/hello", "")
	z, _ := f.svc.Submit(ctx, "o/zeta", "ip", "")
	all, err := f.svc.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	pending, _ := f.svc.List(StatusPending)
	if len(pending) != 1 || pending[0].Repo != "o/zeta" || pending[0].Name != "zeta" || pending[0].Version != "0.1.0" || pending[0].SourceURL != "https://github.com/o/zeta" {
		t.Errorf("pending = %+v", pending)
	}
	v, d, err := f.svc.Get(z.ID)
	if err != nil || v.ID != z.ID || d.ReadmeHTML != "<p># Zeta</p>" {
		t.Errorf("get = %+v %+v %v", v, d, err)
	}
	if _, _, err := f.svc.Get(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get unknown: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./plugins/directory/ -run TestService`
Expected: FAIL — `undefined: NewService`.

- [ ] **Step 3: Write `service.go`**

```go
package directory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// Errors the submit page and the admin API turn into messages and status
// codes. Their text is shown to submitters as-is.
var (
	ErrAlreadyListed = errors.New("this repository is already listed in the directory")
	ErrUnderReview   = errors.New("this repository is already under review")
	ErrNameTaken     = errors.New("a different repository already publishes a plugin with this name")
	ErrRateLimited   = errors.New("too many submissions from your address; try again in an hour")
	ErrBusy          = errors.New("another submission is being checked; try again in a minute")
	ErrNotFound      = errors.New("no such repository")
)

// ValidationError is a submission that failed the plugin contract. Msg is
// the registry's error text, the same message CI used to give.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

const (
	submitLimit  = 5                  // public submissions per address per submitWindow
	submitWindow = time.Hour
	submitBudget = 90 * time.Second   // one public validation, end to end
	ipRetention  = 7 * 24 * time.Hour // how long submitter_ip is kept
)

// Service is the registry: it validates and builds repositories, keeps the
// curation state in the database and serves the current index from memory.
type Service struct {
	db        *gorm.DB
	newSource func(token string) registry.Source
	validator registry.Validator
	siteURL   func() string
	limiter   *ipLimiter

	// validate serializes builds. Public submissions TryLock it (and get
	// ErrBusy), admin operations wait.
	validate sync.Mutex

	mu           sync.RWMutex
	indexRaw     []byte
	indexEntries []registry.IndexEntry
	lastRefresh  time.Time
}

// NewService wires a Service. newSource is called per operation with the
// current github_token setting so a token change needs no restart.
func NewService(db *gorm.DB, newSource func(token string) registry.Source, val registry.Validator, siteURL func() string) *Service {
	return &Service{db: db, newSource: newSource, validator: val, siteURL: siteURL, limiter: newIPLimiter(submitLimit, submitWindow)}
}

// RepoView is a curation record with its build summarised: what the admin
// list shows.
type RepoView struct {
	ID               uint       `json:"id"`
	Repo             string     `json:"repo"`
	Status           string     `json:"status"`
	SubmittedAt      time.Time  `json:"submitted_at"`
	DecidedAt        *time.Time `json:"decided_at"`
	RejectReason     string     `json:"reject_reason"`
	Name             string     `json:"name"`
	DisplayName      string     `json:"display_name"`
	Version          string     `json:"version"`
	Author           string     `json:"author"`
	License          string     `json:"license"`
	Stars            int        `json:"stars"`
	AllowedHosts     []string   `json:"allowed_hosts"`
	MinGoblogVersion string     `json:"min_goblog_version"`
	SourceURL        string     `json:"source_url"`
	BuiltAt          time.Time  `json:"built_at"`
	LastAttemptAt    *time.Time `json:"last_attempt_at"`
	LastError        string     `json:"last_error"`
}

// Submit is the public path: parse, refuse duplicates, rate-limit, validate
// and build synchronously, then queue the repository for review.
func (s *Service) Submit(ctx context.Context, input, ip, token string) (*Repo, error) {
	repo, err := ParseRepo(input)
	if err != nil {
		return nil, err
	}
	if err := s.refuseDuplicate(repo, true); err != nil {
		return nil, err
	}
	if !s.limiter.Allow(ip) {
		return nil, ErrRateLimited
	}
	if !s.validate.TryLock() {
		return nil, ErrBusy
	}
	defer s.validate.Unlock()
	ctx, cancel := context.WithTimeout(ctx, submitBudget)
	defer cancel()
	doc, err := s.build(ctx, repo, token)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	return s.save(repo, doc, StatusPending, ip)
}

// Add is the admin path: same validation, no rate limit, listed at once.
func (s *Service) Add(ctx context.Context, input, token string) (*Repo, error) {
	repo, err := ParseRepo(input)
	if err != nil {
		return nil, err
	}
	if err := s.refuseDuplicate(repo, false); err != nil {
		return nil, err
	}
	s.validate.Lock()
	defer s.validate.Unlock()
	doc, err := s.build(ctx, repo, token)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	r, err := s.save(repo, doc, StatusApproved, "")
	if err != nil {
		return nil, err
	}
	return r, s.regenerate()
}

// refuseDuplicate rejects a repository that is already approved, and (for
// public submissions) one that is already waiting. Rejected ones may be
// submitted again.
func (s *Service) refuseDuplicate(repo string, pendingToo bool) error {
	var existing Repo
	err := s.db.Where("repo = ?", repo).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch existing.Status {
	case StatusApproved:
		return ErrAlreadyListed
	case StatusPending:
		if pendingToo {
			return ErrUnderReview
		}
	}
	return nil
}

func (s *Service) build(ctx context.Context, repo, token string) (registry.DetailDoc, error) {
	return registry.BuildRepo(ctx, s.newSource(token), s.validator, repo, s.siteURL())
}

// save records a successful build in one transaction: the Repo row is
// created or reset to status, and its Build row is created or replaced. A
// plugin name already published by another repository is refused because
// the directory routes on names.
func (s *Service) save(repo string, doc registry.DetailDoc, status, ip string) (*Repo, error) {
	var out Repo
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var taken Build
		err := tx.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
			Where("directory_builds.name = ? AND directory_repos.repo <> ?", doc.Name, repo).First(&taken).Error
		if err == nil {
			return ErrNameTaken
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var r Repo
		if err := tx.Where("repo = ?", repo).First(&r).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now()
		r.Repo, r.Status, r.SubmittedAt, r.SubmitterIP, r.RejectReason, r.DecidedAt = repo, status, now, ip, "", nil
		if status == StatusApproved {
			r.DecidedAt = &now
		}
		if err := tx.Save(&r).Error; err != nil {
			return err
		}

		var b Build
		if err := tx.Where("repo_id = ?", r.ID).First(&b).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		b.RepoID = r.ID
		b.LastAttemptAt = &now
		if err := b.SetDoc(doc); err != nil {
			return err
		}
		if err := tx.Save(&b).Error; err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Approve lists a pending or rejected repository.
func (s *Service) Approve(id uint) error { return s.decide(id, StatusApproved, "") }

// Reject takes a repository off the directory (or keeps it off) with a
// reason only the admin sees.
func (s *Service) Reject(id uint, reason string) error { return s.decide(id, StatusRejected, reason) }

func (s *Service) decide(id uint, status, reason string) error {
	res := s.db.Model(&Repo{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "decided_at": time.Now(), "reject_reason": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return s.regenerate()
}

// Delist forgets a repository entirely; it can be submitted again later.
func (s *Service) Delist(id uint) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&Repo{}, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Where("repo_id = ?", id).Delete(&Build{}).Error
	})
	if err != nil {
		return err
	}
	return s.regenerate()
}

// Rebuild re-validates one repository now (admin action).
func (s *Service) Rebuild(ctx context.Context, id uint, token string) error {
	var r Repo
	if err := s.db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.rebuildRepo(ctx, r, token, false); err != nil {
		return err
	}
	return s.regenerate()
}

// rebuildRepo refreshes one repository's build. With skipUnchanged, a
// repository whose latest release is already built only gets its star
// count refreshed (two API calls instead of a full download). Any failure
// is recorded on the build row and returned; the previous document stays
// so a broken release never takes a plugin off the directory.
func (s *Service) rebuildRepo(ctx context.Context, r Repo, token string, skipUnchanged bool) error {
	var b Build
	if err := s.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil {
		return err
	}
	src := s.newSource(token)
	now := time.Now()
	b.LastAttemptAt = &now

	s.validate.Lock()
	err := func() error {
		if skipUnchanged {
			v, err := registry.LatestVersion(ctx, src, r.Repo)
			if err != nil {
				return err
			}
			if v == b.Version {
				owner, name, _ := strings.Cut(r.Repo, "/")
				stars, err := src.RepoStars(ctx, owner, name)
				if err != nil {
					return err
				}
				d, err := b.Detail()
				if err != nil {
					return err
				}
				d.Stars = stars
				builtAt := b.BuiltAt
				if err := b.SetDoc(d); err != nil {
					return err
				}
				b.BuiltAt = builtAt // the document itself is unchanged
				return nil
			}
		}
		doc, err := s.build(ctx, r.Repo, token)
		if err != nil {
			return err
		}
		return b.SetDoc(doc)
	}()
	s.validate.Unlock()

	if err != nil {
		b.LastError = err.Error()
	}
	if saveErr := s.db.Save(&b).Error; saveErr != nil {
		return saveErr
	}
	return err
}

// RefreshAll is the scheduled rebuild of every approved repository. One
// failure does not stop the others; all are returned joined. It also
// blanks submitter addresses older than ipRetention.
func (s *Service) RefreshAll(ctx context.Context, token string) error {
	var repos []Repo
	if err := s.db.Where("status = ?", StatusApproved).Find(&repos).Error; err != nil {
		return err
	}
	var errs []error
	for _, r := range repos {
		if err := s.rebuildRepo(ctx, r, token, true); err != nil {
			log.Printf("Directory plugin: refresh %s: %v", r.Repo, err)
			errs = append(errs, fmt.Errorf("%s: %w", r.Repo, err))
		}
	}
	if err := s.db.Model(&Repo{}).Where("submitter_ip <> '' AND submitted_at < ?", time.Now().Add(-ipRetention)).
		Update("submitter_ip", "").Error; err != nil {
		errs = append(errs, err)
	}
	s.mu.Lock()
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	if err := s.regenerate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// LastRefresh is when RefreshAll last ran (zero if never).
func (s *Service) LastRefresh() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRefresh
}

// List returns repositories (newest submission first), optionally filtered
// by status.
func (s *Service) List(status string) ([]RepoView, error) {
	q := s.db.Order("submitted_at desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var repos []Repo
	if err := q.Find(&repos).Error; err != nil {
		return nil, err
	}
	var builds []Build
	if err := s.db.Find(&builds).Error; err != nil {
		return nil, err
	}
	byRepo := make(map[uint]*Build, len(builds))
	for i := range builds {
		byRepo[builds[i].RepoID] = &builds[i]
	}
	views := make([]RepoView, 0, len(repos))
	for _, r := range repos {
		views = append(views, view(r, byRepo[r.ID]))
	}
	return views, nil
}

// Get returns one repository with its full document (README and all).
func (s *Service) Get(id uint) (RepoView, registry.DetailDoc, error) {
	var r Repo
	if err := s.db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RepoView{}, registry.DetailDoc{}, ErrNotFound
		}
		return RepoView{}, registry.DetailDoc{}, err
	}
	var b Build
	if err := s.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil {
		return RepoView{}, registry.DetailDoc{}, err
	}
	d, err := b.Detail()
	if err != nil {
		return RepoView{}, registry.DetailDoc{}, err
	}
	return view(r, &b), d, nil
}

func view(r Repo, b *Build) RepoView {
	v := RepoView{ID: r.ID, Repo: r.Repo, Status: r.Status, SubmittedAt: r.SubmittedAt, DecidedAt: r.DecidedAt,
		RejectReason: r.RejectReason, AllowedHosts: []string{}}
	if b == nil {
		return v
	}
	v.BuiltAt, v.LastAttemptAt, v.LastError = b.BuiltAt, b.LastAttemptAt, b.LastError
	if d, err := b.Detail(); err == nil {
		v.Name, v.DisplayName, v.Version, v.Author, v.License = d.Name, d.DisplayName, d.Version, d.Author, d.License
		v.Stars, v.AllowedHosts, v.MinGoblogVersion, v.SourceURL = d.Stars, d.AllowedHosts, d.MinGoblogVersion, d.SourceURL
	}
	return v
}

// Index returns the current index.json bytes and entries, generating them
// on first use.
func (s *Service) Index() ([]byte, []registry.IndexEntry) {
	s.mu.RLock()
	raw, entries := s.indexRaw, s.indexEntries
	s.mu.RUnlock()
	if raw != nil {
		return raw, entries
	}
	if err := s.regenerate(); err != nil {
		log.Printf("Directory plugin: build index: %v", err)
		return []byte("[]\n"), nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexRaw, s.indexEntries
}

// Detail returns the document of an approved plugin by name.
func (s *Service) Detail(name string) (registry.DetailDoc, bool) {
	var b Build
	err := s.db.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
		Where("directory_builds.name = ? AND directory_repos.status = ?", name, StatusApproved).First(&b).Error
	if err != nil {
		return registry.DetailDoc{}, false
	}
	d, err := b.Detail()
	if err != nil {
		log.Printf("Directory plugin: decode %s: %v", name, err)
		return registry.DetailDoc{}, false
	}
	return d, true
}

// regenerate rebuilds the cached index from the approved builds.
func (s *Service) regenerate() error {
	docs, err := approvedDocs(s.db)
	if err != nil {
		return err
	}
	raw, entries := encodeIndex(docs)
	s.mu.Lock()
	s.indexRaw, s.indexEntries = raw, entries
	s.mu.Unlock()
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./plugins/directory/ -run TestService -v`
Expected: PASS ×8. Notes if something fails:
- `Save` with a zero `ID` inserts; with a non-zero one updates — that is what the create-or-reset in `save` relies on.
- `TestService_SubmitRateLimitAndBusy` holds `validate` while calling `Submit`; `Submit` must reach `TryLock` before anything that blocks (it does: duplicate check and limiter first).
- The `Joins` string names the tables explicitly because `Build` and `Repo` override `TableName`.

- [ ] **Step 5: Vet and commit**

```bash
go vet ./plugins/directory/... && git add plugins/directory && git commit -m "directory: registry service (submit, curate, rebuild, index)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: The `directory` plugin renders from the database and hosts `/plugins/submit`

**Files:**
- Modify: `plugins/directory/directory.go`, `plugins/directory/render.go`, `plugins/directory/templates/listing.html`
- Create: `plugins/directory/templates/submit.html`
- Modify: `plugins/directory/submit.go` (handlers), `plugins/directory/submit_test.go`
- Rewrite: `plugins/directory/directory_test.go`
- Leave `fetcher.go` / `fetcher_test.go` in place (Task 9 moves them); nothing in the plugin references `Fetcher` after this task.

**Interfaces:**
- Consumes: `Service` (Task 7), `Migrate`, `blog.Setting`, `gplugin.PluginSetting`.
- Produces:
  ```go
  func New() *Plugin
  func (p *Plugin) SetUserAgent(ua string)
  func (p *Plugin) Service() *Service          // nil before OnInit
  func (p *Plugin) Token() string              // github_token setting
  func (p *Plugin) Hosted() bool               // OnInit ran and enabled == "true"
  ```
  Settings: `enabled`, `refresh_minutes` (360), `github_token` (password).

- [ ] **Step 1: Rewrite `directory_test.go`**

Replace the whole file with:

```go
package directory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"goblog/blog"
	gplugin "goblog/plugin"
	"goblog/plugins/directory/registry"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "directory" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	defaults, types := map[string]string{}, map[string]string{}
	for _, s := range p.Settings() {
		defaults[s.Key], types[s.Key] = s.DefaultValue, s.Type
	}
	if defaults["enabled"] != "false" || defaults["refresh_minutes"] != "360" || defaults["github_token"] != "" || types["github_token"] != "password" {
		t.Errorf("settings: defaults=%v types=%v", defaults, types)
	}
	if _, ok := defaults["index_url"]; ok {
		t.Error("index_url is gone: the directory is built here, not mirrored")
	}
	pages := p.Pages()
	if len(pages) != 1 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
	}
	var _ gplugin.Plugin = p
}

func TestRefreshInterval(t *testing.T) {
	cases := map[string]time.Duration{"": 360 * time.Minute, "abc": 360 * time.Minute, "0": 360 * time.Minute, "-3": 360 * time.Minute,
		"5": 15 * time.Minute, "15": 15 * time.Minute, "60": 60 * time.Minute}
	for in, want := range cases {
		if got := refreshInterval(map[string]string{"refresh_minutes": in}); got != want {
			t.Errorf("refreshInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

// pluginFixture is an initialised plugin over sqlite with the fake GitHub
// from service_test.go behind it.
type pluginFixture struct {
	*fixture
	p *Plugin
}

func newPluginFixture(t *testing.T) *pluginFixture {
	t.Helper()
	f := newFixture(t)
	f.db.AutoMigrate(&blog.Page{}, &blog.Setting{}, &gplugin.PluginSetting{})
	f.db.Create(&blog.Setting{Key: "site_url", Value: "https://example.test"})
	p := New()
	p.newSource = func(string) registry.Source { return f.src }
	p.validator = f.val
	if err := p.OnInit(f.db); err != nil {
		t.Fatal(err)
	}
	f.svc = p.Service()
	return &pluginFixture{fixture: f, p: p}
}

func TestOnInit_MigratesCreatesPageAndService(t *testing.T) {
	f := newPluginFixture(t)
	if err := f.p.OnInit(f.db); err != nil {
		t.Fatal(err)
	}
	var pages []blog.Page
	f.db.Where("page_type = ?", PageType).Find(&pages)
	if len(pages) != 1 || pages[0].Slug != "plugins" || pages[0].Title != "Plugins" || !pages[0].ShowInNav || pages[0].NavOrder != 30 || !pages[0].Enabled {
		t.Errorf("pages: %+v", pages)
	}
	if f.p.Service() == nil || !f.db.Migrator().HasTable(&Repo{}) || !f.db.Migrator().HasTable(&Build{}) {
		t.Error("OnInit must migrate the tables and create the service")
	}
	if f.p.Hosted() {
		t.Error("not hosted until enabled")
	}
	f.db.Create(&gplugin.PluginSetting{PluginName: "directory", Key: "enabled", Value: "true"})
	f.db.Create(&gplugin.PluginSetting{PluginName: "directory", Key: "github_token", Value: "tok"})
	if !f.p.Hosted() || f.p.Token() != "tok" {
		t.Errorf("hosted=%v token=%q", f.p.Hosted(), f.p.Token())
	}
}

func TestOnInit_SlugCollision(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&blog.Page{})
	existing := blog.Page{Title: "Mine", Slug: "plugins", PageType: blog.PageTypeCustom, Enabled: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatalf("OnInit must not fail when the slug is already taken, got %v", err)
	}
	var pages []blog.Page
	db.Where("slug = ?", "plugins").Find(&pages)
	if len(pages) != 1 || pages[0].PageType != blog.PageTypeCustom {
		t.Errorf("the pre-existing page must be left alone: %+v", pages)
	}
}

func TestScheduledJob(t *testing.T) {
	f := newPluginFixture(t)
	r, _ := f.svc.Add(context.Background(), "o/hello", "")
	jobs := f.p.ScheduledJobs()
	if len(jobs) != 1 || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs: %+v", jobs)
	}
	f.src.repos["o/hello"].stars = 50

	// Disabled: nothing happens.
	if err := jobs[0].Run(f.db, map[string]string{"enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	if !f.svc.LastRefresh().IsZero() {
		t.Fatal("disabled plugin must not refresh")
	}
	// Enabled and never refreshed: refreshes.
	if err := jobs[0].Run(f.db, map[string]string{"enabled": "true", "refresh_minutes": "15"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := f.svc.Get(r.ID); v.Stars != 50 {
		t.Errorf("stars after refresh = %d", v.Stars)
	}
	// Fresh: no-op.
	f.src.repos["o/hello"].stars = 51
	jobs[0].Run(f.db, map[string]string{"enabled": "true", "refresh_minutes": "15"})
	if v, _, _ := f.svc.Get(r.ID); v.Stars != 50 {
		t.Error("a fresh index must not be refreshed again")
	}
}

func newRenderCtx(t *testing.T, method, path, subPath string, form url.Values) (*gplugin.HookContext, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if form != nil {
		c.Request = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		c.Request = httptest.NewRequest(method, path, nil)
	}
	c.Request.RemoteAddr = "203.0.113.5:1234"
	return &gplugin.HookContext{GinContext: c, Settings: map[string]string{"enabled": "true"}, SubPath: subPath}, w
}

func content(t *testing.T, data gin.H) string {
	t.Helper()
	html, _ := data["plugin_content"].(string)
	if data["has_plugin_content"] != true || html == "" {
		t.Fatalf("expected plugin content, got %v", data)
	}
	return html
}

func TestRenderPage_Listing(t *testing.T) {
	f := newPluginFixture(t)
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	if tmpl, _ := f.p.RenderPage(ctx, "other"); tmpl != "" {
		t.Errorf("other page types are declined, got %q", tmpl)
	}

	// Empty directory.
	tmpl, data := f.p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || !strings.Contains(content(t, data), "directory is empty") {
		t.Errorf("empty listing: %q %v", tmpl, data)
	}

	f.svc.Add(context.Background(), "o/zeta", "")
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	_, data = f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	for _, want := range []string{`href="/plugins/hello"`, `href="/plugins/zeta"`, "HELLO", "Says hi.", "1.0.0", "Jason", "MIT",
		`href="/plugins/index.json"`, `href="https://github.com/o/hello"`, `href="/plugins/submit"`, "★ 7"} {
		if !strings.Contains(html, want) {
			t.Errorf("listing missing %q in:\n%s", want, html)
		}
	}
	if strings.Index(html, "/plugins/hello") > strings.Index(html, "/plugins/zeta") {
		t.Error("listing must be sorted by stars, hello (7) before zeta (1)")
	}
	if strings.Contains(html, "issues/new") {
		t.Error("the GitHub issue submission box is gone")
	}
}

func TestRenderPage_ListingEscapesStrings(t *testing.T) {
	f := newPluginFixture(t)
	r := seed(t, f.db, "o/evil", StatusApproved, func() registry.DetailDoc {
		d := doc("evil", "1.0.0", 0)
		d.DisplayName, d.SourceURL = "<script>alert(1)</script>", "javascript:alert(1)"
		return d
	}())
	_ = r
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if strings.Contains(html, "<script>alert(1)") || strings.Contains(html, `href="javascript:`) {
		t.Errorf("unescaped output:\n%s", html)
	}
}

func TestRenderPage_IndexJSON(t *testing.T) {
	f := newPluginFixture(t)
	ctx, w := newRenderCtx(t, http.MethodGet, "/plugins/index.json", "index.json", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
		t.Errorf("raw responses return no template, got %q", tmpl)
	}
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("empty index: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, w = newRenderCtx(t, http.MethodGet, "/plugins/index.json", "index.json", nil)
	f.p.RenderPage(ctx, PageType)
	raw, _ := f.svc.Index()
	if w.Code != http.StatusOK || w.Body.String() != string(raw) || w.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("index.json: %d %q cache=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
}

func TestRenderPage_DetailAndDetailJSON(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), "o/hello", "")
	z, _ := f.svc.Submit(context.Background(), "o/zeta", "ip", "")

	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/hello", "hello", nil)
	tmpl, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if tmpl != "page_content.html" || data["title"] != "HELLO" {
		t.Errorf("detail: %q title=%v", tmpl, data["title"])
	}
	for _, want := range []string{"<p># Hello</p>", "1.0.0", "MIT", `href="https://github.com/o/hello"`, "0.3.0", "No network access", "<p>notes</p>",
		`href="https://github.com/o/hello/releases/download/v1.0.0/plugin.wasm"`, registry.Sum([]byte("\x00asm hello"))} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q in:\n%s", want, html)
		}
	}

	ctx, w := newRenderCtx(t, http.MethodGet, "/plugins/hello.json", "hello.json", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"readme_html": "<p># Hello</p>"`) {
		t.Errorf("hello.json: %q %d %s", tmpl, w.Code, w.Body.String())
	}

	// Pending, unknown and malformed names are declined (blog renders 404).
	for _, sp := range []string{"zeta", "zeta.json", "nope", "nope.json", "Bad Name", "../x"} {
		ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/"+sp, sp, nil)
		if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
			t.Errorf("%q should be declined, got %q", sp, tmpl)
		}
	}
	_ = z
}

func TestRenderPage_DetailFlagsBroadHosts(t *testing.T) {
	f := newPluginFixture(t)
	seed(t, f.db, "o/wide", StatusApproved, func() registry.DetailDoc {
		d := doc("wide", "1.0.0", 0)
		d.AllowedHosts = []string{"api.example.test", "*.example.test", "localhost"}
		return d
	}())
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/wide", "wide", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	for _, want := range []string{"api.example.test", "wildcard", "local network"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in:\n%s", want, html)
		}
	}
}

func TestHostNote(t *testing.T) {
	cases := map[string]string{"api.example.test": "", "*": "any host", "*.example.test": "wildcard", "localhost": "local network",
		"127.0.0.1": "local network", "10.1.2.3": "local network", "172.20.0.1": "local network", "192.168.1.1:8080": "local network", "172.15.0.1": ""}
	for in, want := range cases {
		if got := hostNote(in); got != want {
			t.Errorf("hostNote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBasePathFollowsSlug(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, _ := newRenderCtx(t, http.MethodGet, "/extensions", "", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	if html := content(t, data); !strings.Contains(html, `href="/extensions/hello"`) || !strings.Contains(html, `href="/extensions/submit"`) {
		t.Errorf("links must follow the page slug:\n%s", html)
	}
}
```

- [ ] **Step 2: Write `submit_test.go` handler tests** (append to the file from Task 6)

```go
func TestRenderPage_SubmitForm(t *testing.T) {
	f := newPluginFixture(t)
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/submit", "submit", nil)
	tmpl, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if tmpl != "page_content.html" || data["title"] != "Submit a plugin" {
		t.Errorf("form: %q %v", tmpl, data["title"])
	}
	for _, want := range []string{`<form`, `method="post"`, `action="/plugins/submit"`, `name="repo"`, `name="website"`, "goblog-plugin.json", "PLUGIN_CONTRACT.md"} {
		if !strings.Contains(html, want) {
			t.Errorf("form missing %q in:\n%s", want, html)
		}
	}
}

func TestRenderPage_SubmitPost(t *testing.T) {
	f := newPluginFixture(t)
	post := func(repo, honeypot string) (gin.H, string) {
		ctx, _ := newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {repo}, "website": {honeypot}})
		_, data := f.p.RenderPage(ctx, PageType)
		return data, content(t, data)
	}

	// Success: queued, row pending.
	_, html := post("https://github.com/o/hello", "")
	if !strings.Contains(html, "Queued for review") {
		t.Errorf("success page:\n%s", html)
	}
	var r Repo
	if err := f.db.Where("repo = ?", "o/hello").First(&r).Error; err != nil || r.Status != StatusPending || r.SubmitterIP != "203.0.113.5" {
		t.Errorf("row = %+v %v", r, err)
	}

	// Under review, then listed.
	if _, html = post("o/hello", ""); !strings.Contains(html, "already under review") {
		t.Errorf("under review:\n%s", html)
	}
	f.svc.Approve(r.ID)
	if _, html = post("o/hello", ""); !strings.Contains(html, "already listed") || !strings.Contains(html, `href="/plugins/hello"`) {
		t.Errorf("already listed:\n%s", html)
	}

	// Validation failure: the registry's message and the form again, prefilled.
	if _, html = post("o/missing", ""); !strings.Contains(html, "no such repository") || !strings.Contains(html, `value="o/missing"`) {
		t.Errorf("validation failure:\n%s", html)
	}
	// Bad input.
	if _, html = post("gitlab.com/o/r", ""); !strings.Contains(html, "enter a GitHub repository URL") {
		t.Errorf("bad input:\n%s", html)
	}
	// Honeypot: success page, nothing stored.
	if _, html = post("o/zeta", "http://spam"); !strings.Contains(html, "Queued for review") {
		t.Errorf("honeypot:\n%s", html)
	}
	if err := f.db.Where("repo = ?", "o/zeta").First(&Repo{}).Error; err == nil {
		t.Error("honeypot submissions must not be stored")
	}
}

func TestRenderPage_SubmitRateLimitedIs429(t *testing.T) {
	f := newPluginFixture(t)
	var w *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		var ctx *gplugin.HookContext
		ctx, w = newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {"o/missing"}})
		f.p.RenderPage(ctx, PageType)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("6th attempt should set 429, got %d", w.Code)
	}
}
```

Add `"net/http"`, `"net/http/httptest"`, `"net/url"`, `"strings"`, `"github.com/gin-gonic/gin"`, `gplugin "goblog/plugin"` to the test file's imports.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./plugins/directory/ 2>&1 | head`
Expected: compile errors (`p.newSource`, `p.validator`, `Hosted`, `newRenderCtx` signature …).

- [ ] **Step 4: Rewrite `directory.go`**

```go
// Package directory is the plugin directory: the registry behind
// goblog.live/plugins. Repositories are submitted at /plugins/submit,
// validated and built here, approved in Admin → Plugins, and served as the
// listing, per-plugin pages and the machine-readable /plugins/index.json
// that every goblog's installer reads. It is compiled in and disabled by
// default; any goblog can host a directory by enabling it.
package directory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"goblog/blog"
	gplugin "goblog/plugin"
	"goblog/plugins/directory/registry"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PageType is the page type this plugin owns.
const PageType = "plugin-directory"

const (
	defaultRefresh = 360 * time.Minute
	minRefresh     = 15 * time.Minute
)

// Plugin is the directory plugin. Its Service exists once OnInit has run.
type Plugin struct {
	gplugin.BasePlugin
	userAgent string
	db        *gorm.DB
	svc       *Service
	validator registry.Validator
	newSource func(token string) registry.Source // nil → GitHub; tests inject a fake
}

// New creates the directory plugin.
func New() *Plugin {
	return &Plugin{userAgent: "goblog-directory", validator: registry.WasmValidator{}}
}

func (p *Plugin) Name() string        { return "directory" }
func (p *Plugin) DisplayName() string { return "Plugin Directory" }
func (p *Plugin) Version() string     { return "2.0.0" }

// SetUserAgent sets the User-Agent sent to GitHub.
func (p *Plugin) SetUserAgent(ua string) { p.userAgent = ua }

// Service is the registry behind the pages; nil until OnInit has run.
func (p *Plugin) Service() *Service { return p.svc }

func (p *Plugin) Settings() []gplugin.SettingDefinition {
	return []gplugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled",
			Description: "Set to 'true' to host a plugin directory at /plugins"},
		{Key: "refresh_minutes", Type: "text", DefaultValue: "360", Label: "Refresh interval (minutes)",
			Description: "How often listed plugins are checked for new releases and stars (minimum 15)"},
		{Key: "github_token", Type: "password", DefaultValue: "", Label: "GitHub token",
			Description: "Optional. A token with no scopes raises the GitHub API limit from 60 to 5000 requests per hour; without one the directory still works but refreshes slowly."},
	}
}

func (p *Plugin) Pages() []gplugin.PageDefinition {
	return []gplugin.PageDefinition{{
		PageType:    PageType,
		Title:       "Plugins",
		Slug:        "plugins",
		ShowInNav:   true,
		NavOrder:    30,
		Description: "Browsable directory of goblog plugins",
	}}
}

// source returns the GitHub client for one operation, with the token the
// admin configured (if any).
func (p *Plugin) source(token string) registry.Source {
	if p.newSource != nil {
		return p.newSource(token)
	}
	g := registry.NewGitHubSource(token, "")
	g.SetUserAgent(p.userAgent)
	return g
}

// OnInit migrates the registry tables, creates the Service and ensures the
// directory page exists. The registry's ensurePages already creates the
// page for every plugin before OnInit runs, so this normally finds the row
// in place and is kept as a fallback. The admin can rename or reorder it.
//
// blog.Page.Slug has a unique index, so if some other page already uses the
// "plugins" slug (a different page type), creating our page would fail.
// That must not abort plugin.Registry.Init, so we log a warning and leave
// it to the operator instead of returning an error.
func (p *Plugin) OnInit(db *gorm.DB) error {
	if err := Migrate(db); err != nil {
		return fmt.Errorf("directory plugin: migrate: %w", err)
	}
	p.db = db
	if p.svc == nil {
		p.svc = NewService(db, p.source, p.validator, func() string { return siteURL(db) })
	}

	var page blog.Page
	err := db.Where("page_type = ?", PageType).First(&page).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query page: %w", err)
	}
	def := p.Pages()[0]
	var existing blog.Page
	if err := db.Where("slug = ?", def.Slug).First(&existing).Error; err == nil {
		log.Printf("Directory plugin: page slug %q is already used by a %q page; rename it and restart to create the plugin directory page", def.Slug, existing.PageType)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("directory plugin: query slug: %w", err)
	}
	page = blog.Page{
		Title:     def.Title,
		Slug:      def.Slug,
		PageType:  def.PageType,
		ShowInNav: def.ShowInNav,
		NavOrder:  def.NavOrder,
		Enabled:   true,
	}
	if err := db.Create(&page).Error; err != nil {
		return fmt.Errorf("directory plugin: create page: %w", err)
	}
	log.Println("Directory plugin: created plugins page")
	return nil
}

// siteURL is the configured site_url setting ("" when unset); detail_url
// in the index is built from it.
func siteURL(db *gorm.DB) string {
	var s blog.Setting
	if err := db.Where("key = ?", "site_url").First(&s).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(s.Value)
}

// settings reads this plugin's settings outside a hook (the admin API
// needs the token and the enabled flag).
func (p *Plugin) settings() map[string]string {
	out := map[string]string{}
	if p.db == nil {
		return out
	}
	var rows []gplugin.PluginSetting
	p.db.Where("plugin_name = ?", p.Name()).Find(&rows)
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out
}

// Token is the configured github_token ("" for anonymous access).
func (p *Plugin) Token() string { return p.settings()["github_token"] }

// Hosted reports whether this site runs a directory: initialised and enabled.
func (p *Plugin) Hosted() bool { return p.svc != nil && p.settings()["enabled"] == "true" }

// ScheduledJobs ticks every minute and refreshes every listed plugin once
// the last refresh is older than refresh_minutes, so the setting takes
// effect without a restart.
func (p *Plugin) ScheduledJobs() []gplugin.ScheduledJob {
	return []gplugin.ScheduledJob{{
		Name:     "refresh-directory",
		Interval: time.Minute,
		Run: func(_ *gorm.DB, settings map[string]string) error {
			if settings["enabled"] != "true" || p.svc == nil {
				return nil
			}
			if time.Since(p.svc.LastRefresh()) < refreshInterval(settings) {
				return nil
			}
			return p.svc.RefreshAll(context.Background(), settings["github_token"])
		},
	}}
}

const unavailableHTML = `<div class="alert alert-warning" role="alert">The plugin directory is unavailable right now. Please check back later.</div>`

// RenderPage serves the listing (""), the raw index ("index.json"), the
// submission page ("submit"), one plugin's page ("<name>") and its JSON
// ("<name>.json"). Anything else — including plugins that are not approved
// — is declined, which blog turns into a 404.
func (p *Plugin) RenderPage(ctx *gplugin.HookContext, pageType string) (string, gin.H) {
	if pageType != PageType || p.svc == nil {
		return "", nil
	}
	c := ctx.GinContext
	base := basePath(c)

	switch {
	case ctx.SubPath == "":
		_, entries := p.svc.Index()
		sorted := append([]Entry(nil), entries...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Stars != sorted[j].Stars {
				return sorted[i].Stars > sorted[j].Stars
			}
			return sorted[i].Name < sorted[j].Name
		})
		html, err := renderListing(base, sorted)
		if err != nil {
			log.Printf("Directory plugin: render listing: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html}

	case ctx.SubPath == "index.json":
		raw, _ := p.svc.Index()
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case ctx.SubPath == "submit":
		return p.renderSubmit(ctx, base)

	case strings.HasSuffix(ctx.SubPath, ".json") && validName(strings.TrimSuffix(ctx.SubPath, ".json")):
		d, ok := p.svc.Detail(strings.TrimSuffix(ctx.SubPath, ".json"))
		if !ok {
			return "", nil
		}
		raw, err := marshalJSON(d)
		if err != nil {
			log.Printf("Directory plugin: encode %s: %v", d.Name, err)
			return "", nil
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/json", raw)
		return "", nil

	case validName(ctx.SubPath):
		d, ok := p.svc.Detail(ctx.SubPath)
		if !ok {
			return "", nil
		}
		html, err := renderDetail(base, d.IndexEntry, &d, "")
		if err != nil {
			log.Printf("Directory plugin: render %s: %v", d.Name, err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML, "title": d.DisplayName}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html, "title": d.DisplayName}
	}
	return "", nil
}

// refreshInterval reads refresh_minutes: unparseable or non-positive falls
// back to the default; anything below the minimum is raised to it, because
// every refresh spends GitHub API quota on every listed plugin.
func refreshInterval(settings map[string]string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(settings["refresh_minutes"]))
	if err != nil || n <= 0 {
		return defaultRefresh
	}
	if d := time.Duration(n) * time.Minute; d >= minRefresh {
		return d
	}
	return minRefresh
}
```

- [ ] **Step 5: Add the submit handlers to `submit.go`**

Append to `submit.go` (add imports `"errors"` already present, plus `"log"`, `"net/http"`, `gplugin "goblog/plugin"`, `"github.com/gin-gonic/gin"`):

```go
// submitView is what templates/submit.html renders.
type submitView struct {
	Base    string
	Repo    string // prefilled input
	Error   string // shown above the form
	Done    bool   // queued: show the confirmation instead of the form
	Listed  string // "already listed": the plugin's page path
}

// renderSubmit serves the submission form (GET) and handles it (POST).
// The form is plain HTML so it works without JavaScript; the result is
// always rendered into the page, with 429 as the only non-200 status.
func (p *Plugin) renderSubmit(ctx *gplugin.HookContext, base string) (string, gin.H) {
	c := ctx.GinContext
	page := func(v submitView) (string, gin.H) {
		html, err := renderSubmitPage(v)
		if err != nil {
			log.Printf("Directory plugin: render submit: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML, "title": "Submit a plugin"}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html, "title": "Submit a plugin"}
	}
	if c.Request.Method != http.MethodPost {
		return page(submitView{Base: base})
	}

	repo := strings.TrimSpace(c.PostForm("repo"))
	// "website" is a honeypot: humans never see it, bots fill it. Pretend
	// it worked so they move on.
	if c.PostForm("website") != "" {
		return page(submitView{Base: base, Done: true})
	}
	_, err := p.svc.Submit(c.Request.Context(), repo, c.ClientIP(), ctx.Settings["github_token"])
	switch {
	case err == nil:
		return page(submitView{Base: base, Done: true})
	case errors.Is(err, ErrAlreadyListed):
		if key, perr := ParseRepo(repo); perr == nil {
			if name, ok := p.svc.nameOf(key); ok {
				return page(submitView{Base: base, Repo: repo, Error: err.Error(), Listed: base + "/" + name})
			}
		}
		return page(submitView{Base: base, Repo: repo, Error: err.Error()})
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrBusy):
		c.Status(http.StatusTooManyRequests)
		return page(submitView{Base: base, Repo: repo, Error: err.Error()})
	default:
		// ErrBadRepo, ErrUnderReview, ErrNameTaken, *ValidationError: all
		// carry a message meant for the submitter. Anything else is an
		// operator problem and is logged, not shown.
		var ve *ValidationError
		if errors.As(err, &ve) || errors.Is(err, ErrBadRepo) || errors.Is(err, ErrUnderReview) || errors.Is(err, ErrNameTaken) {
			return page(submitView{Base: base, Repo: repo, Error: err.Error()})
		}
		log.Printf("Directory plugin: submit %q: %v", repo, err)
		return page(submitView{Base: base, Repo: repo, Error: "Something went wrong on our side; please try again later."})
	}
}
```

And in `service.go` add:

```go
// nameOf returns the plugin name a repository publishes, if it has a build.
func (s *Service) nameOf(repo string) (string, bool) {
	var b Build
	err := s.db.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
		Where("directory_repos.repo = ?", repo).First(&b).Error
	return b.Name, err == nil
}
```

- [ ] **Step 6: Add `renderSubmitPage` to `render.go` and the templates**

`render.go`, after `renderDetail`:

```go
func renderSubmitPage(v submitView) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "submit.html", v)
	return buf.String(), err
}
```

`templates/submit.html`:

```html
<p><a href="{{ .Base }}">&larr; All plugins</a></p>
{{ if .Done }}
<div class="alert alert-success" role="alert">
  <strong>Queued for review.</strong> Your repository passed validation. It appears on this page once a maintainer approves it — usually within a few days.
</div>
{{ else }}
<p>A plugin is a GitHub repository with a <code>goblog-plugin.json</code> manifest, a <code>README.md</code>, and releases tagged <code>vX.Y.Z</code> with the compiled <code>plugin.wasm</code> attached — see the <a href="https://github.com/goblogplatform/goblog/blob/main/docs/PLUGIN_CONTRACT.md">plugin contract</a>. Paste the repository URL; it is checked right away and queued for a maintainer to approve.</p>
{{ if .Error }}
<div class="alert alert-danger" role="alert">{{ .Error }}{{ if .Listed }} — <a href="{{ .Listed }}">see its page</a>.{{ end }}</div>
{{ end }}
<form method="post" action="{{ .Base }}/submit" class="row g-2">
  <div class="col-sm-9"><input type="text" class="form-control" name="repo" value="{{ .Repo }}" placeholder="https://github.com/you/goblog-plugin-yours" required></div>
  <div class="col-sm-3 d-grid"><button type="submit" class="btn btn-primary">Submit for review</button></div>
  <div class="d-none" aria-hidden="true"><label>Website <input type="text" name="website" tabindex="-1" autocomplete="off"></label></div>
</form>
<p class="mt-3 small text-muted">Validation downloads the latest release's module, loads it in goblog's sandbox and checks that its name and version match the manifest and the release tag. Nothing is stored unless it passes.</p>
{{ end }}
```

`templates/listing.html`: replace everything from `<div class="card mt-4">` to the end of the file with:

```html
<p class="mt-4">Built a plugin? <a href="{{ .Base }}/submit">Submit it to the directory</a>.</p>
```

Also change the empty-state: replace the first `<p>` in `listing.html` with

```html
{{ if .Entries }}
<p>{{ len .Entries }} plugin{{ if ne (len .Entries) 1 }}s{{ end }} in the directory, most-starred first.
Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.
Install them from your own goblog under <strong>Admin → Plugins</strong>.</p>
{{ else }}
<p>The plugin directory is empty. Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.</p>
{{ end }}
```

(and keep the existing `{{ if .Entries }}<table>…{{ end }}` block).

- [ ] **Step 7: Run the package tests**

Run: `go test ./plugins/directory/... -v 2>&1 | tail -40`
Expected: every test PASS, including the untouched `TestFetcher_*`. Common causes if not:
- `c.ClientIP()` on a `gin.CreateTestContext` context returns `RemoteAddr`'s host when no trusted-proxy config is set — `203.0.113.5` as the test expects.
- If `gin` complains about the engine being nil for `ClientIP`, use `c.Request.RemoteAddr` split on the last `:` instead and adjust the comment.

- [ ] **Step 8: Vet, build, commit**

```bash
go vet ./... && go build ./... && git add plugins/directory && git commit -m "directory: render from the database and host /plugins/submit

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Move `Fetcher` into `plugin/installer`

**Files:**
- Move: `plugins/directory/fetcher.go` → `plugin/installer/fetcher.go`; `plugins/directory/fetcher_test.go` → `plugin/installer/fetcher_test.go`
- Modify: `plugin/installer/installer.go`, `plugin/installer/installer_test.go`, `admin/plugins_test.go`, `goblog.go`

**Interfaces:**
- Produces: `installer.NewFetcher(client *http.Client) *installer.Fetcher` with the same methods (`SetUserAgent`, `Refresh`, `Ensure`, `Index`, `Entry`, `FetchedAt`, `Detail`) over `directory.Entry`/`directory.Detail`.

- [ ] **Step 1: Move the files**

```bash
git mv plugins/directory/fetcher.go plugin/installer/fetcher.go
git mv plugins/directory/fetcher_test.go plugin/installer/fetcher_test.go
```

In both files change `package directory` to `package installer`, add `"goblog/plugins/directory"` to the imports, and qualify the wire types: `[]Entry` → `[]directory.Entry`, `map[string]Entry` → `map[string]directory.Entry`, `*Detail` → `*directory.Detail`, `Detail{}` → `directory.Detail{}`, `Entry{}` → `directory.Entry{}` (in the test file too). Update the `Fetcher` doc comment's first line to: `// Fetcher keeps an in-memory copy of a remote directory's index (goblog.live's by default) and of the detail JSON for plugins that have been viewed.` Change the log prefix `"Directory plugin: initial index fetch failed"` to `"Plugin installer: initial index fetch failed"`.

- [ ] **Step 2: Point the installer, tests and main at the new type**

`plugin/installer/installer.go`: `Directory *directory.Fetcher` → `Directory *Fetcher` (keep the comment). `plugin/installer/installer_test.go` and `admin/plugins_test.go`: `directory.NewFetcher(` → `installer.NewFetcher(` (in `installer_test.go` it is just `NewFetcher(`; drop the now-unused `goblog/plugins/directory` import if the file has no other use of it — `admin/plugins_test.go` keeps it only if still referenced). `goblog.go`: `Directory:   directory.NewFetcher(nil),` → `Directory:   installer.NewFetcher(nil),`.

- [ ] **Step 3: Build and run everything**

Run: `go build ./... && go vet ./... && go test ./plugin/... ./plugins/... ./admin/...`
Expected: PASS. `plugins/directory` no longer imports `net/http` for fetching; if `go vet` flags an unused import there, remove it.

- [ ] **Step 4: Commit**

```bash
git add -A plugins/directory plugin/installer admin goblog.go && git commit -m "installer: own the index fetcher

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Admin API for the directory

**Files:**
- Create: `admin/directory.go`, `admin/directory_test.go`
- Modify: `admin/admin.go` (field), `admin/plugins.go` (`PluginStatus`), `plugin/installer/installer.go` (`Status.DirectoryHosted`), `plugins/directory/directory.go` (two exported setters)

**Interfaces:**
- Consumes: `directory.Service` methods (Task 7), `(*directory.Plugin).Service/Token/Hosted` (Task 8).
- Produces:
  ```go
  // admin.Admin
  Directory *directory.Plugin   // nil when this site does not host a directory
  func (a *Admin) ListDirectoryRepos(c *gin.Context)     // GET  /api/v1/directory/repos?status=
  func (a *Admin) GetDirectoryRepo(c *gin.Context)       // GET  /api/v1/directory/repos/:id   → {"repo": RepoView, "detail": DetailDoc}
  func (a *Admin) AddDirectoryRepo(c *gin.Context)       // POST /api/v1/directory/repos {repo}
  func (a *Admin) ApproveDirectoryRepo(c *gin.Context)   // POST /api/v1/directory/repos/:id/approve
  func (a *Admin) RejectDirectoryRepo(c *gin.Context)    // POST /api/v1/directory/repos/:id/reject {reason}
  func (a *Admin) RebuildDirectoryRepo(c *gin.Context)   // POST /api/v1/directory/repos/:id/rebuild
  func (a *Admin) DelistDirectoryRepo(c *gin.Context)    // DELETE /api/v1/directory/repos/:id
  // plugins/directory
  func (p *Plugin) SetSource(f func(token string) registry.Source)
  func (p *Plugin) SetValidator(v registry.Validator)
  // installer.Status
  DirectoryHosted bool `json:"directory_hosted"`
  ```

- [ ] **Step 1: Add the setters to `plugins/directory/directory.go`**

```go
// SetSource replaces the GitHub client factory (tests, or a mirror).
func (p *Plugin) SetSource(f func(token string) registry.Source) { p.newSource = f }

// SetValidator replaces the module validator (tests).
func (p *Plugin) SetValidator(v registry.Validator) { p.validator = v }
```

- [ ] **Step 2: Write `admin/directory_test.go`**

```go
package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubSource is an in-memory registry.Source with one repository, o/hello
// v1.0.0, whose plugin.wasm the FakeValidator below recognises.
type stubSource struct{ missing bool }

var helloWasm = []byte("\x00asm hello")

func (s stubSource) Releases(_ context.Context, owner, repo string) ([]registry.Release, error) {
	if s.missing || owner+"/"+repo != "o/hello" {
		return nil, fmt.Errorf("%s/%s: no such repository", owner, repo)
	}
	return []registry.Release{{Tag: "v1.0.0", Body: "notes", URL: "https://github.com/o/hello/releases/tag/v1.0.0",
		PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		Assets:      []registry.Asset{{ID: 1, Name: "plugin.wasm", Size: len(helloWasm), DownloadURL: "https://github.com/o/hello/releases/download/v1.0.0/plugin.wasm"}}}}, nil
}
func (s stubSource) File(_ context.Context, _, _, _, path string) ([]byte, error) {
	switch path {
	case "goblog-plugin.json":
		return []byte(`{"name":"hello","display_name":"Hello","description":"Says hi.","author":"Jason","license":"MIT","runtime":"wasm","min_goblog_version":"0.3.0"}`), nil
	case "README.md":
		return []byte("# Hello"), nil
	}
	return nil, registry.ErrNotFound
}
func (s stubSource) ReleaseAsset(context.Context, string, string, int64) ([]byte, error) { return helloWasm, nil }
func (s stubSource) RenderMarkdown(_ context.Context, _, md string) (string, error) {
	return "<p>" + md + "</p>", nil
}
func (s stubSource) RepoStars(context.Context, string, string) (int, error) { return 7, nil }

type directoryHarness struct {
	router *gin.Engine
	auth   *Auth
	db     *gorm.DB
	dir    *directory.Plugin
	src    *stubSource
}

func newDirectoryHarness(t *testing.T, wire bool) *directoryHarness {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	src := &stubSource{}
	dir := directory.New()
	dir.SetSource(func(string) registry.Source { return src })
	dir.SetValidator(&registry.FakeValidator{Infos: map[string]plugin.Info{
		registry.Sum(helloWasm): {Name: "hello", DisplayName: "Hello", Version: "1.0.0", Runtime: "wasm"},
	}})
	if wire {
		if err := dir.OnInit(db); err != nil {
			t.Fatal(err)
		}
		ad.Directory = dir
	}

	router := gin.New()
	router.GET("/api/v1/directory/repos", ad.ListDirectoryRepos)
	router.POST("/api/v1/directory/repos", ad.AddDirectoryRepo)
	router.GET("/api/v1/directory/repos/:id", ad.GetDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/approve", ad.ApproveDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/reject", ad.RejectDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/rebuild", ad.RebuildDirectoryRepo)
	router.DELETE("/api/v1/directory/repos/:id", ad.DelistDirectoryRepo)
	return &directoryHarness{router: router, auth: a, db: db, dir: dir, src: src}
}

func (h *directoryHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

var directoryRoutes = []struct{ m, p string }{
	{"GET", "/api/v1/directory/repos"}, {"POST", "/api/v1/directory/repos"}, {"GET", "/api/v1/directory/repos/1"},
	{"POST", "/api/v1/directory/repos/1/approve"}, {"POST", "/api/v1/directory/repos/1/reject"},
	{"POST", "/api/v1/directory/repos/1/rebuild"}, {"DELETE", "/api/v1/directory/repos/1"},
}

func TestDirectoryAPI_NonAdmin(t *testing.T) {
	h := newDirectoryHarness(t, true)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	for _, r := range directoryRoutes {
		if w := h.do(r.m, r.p, `{"repo":"o/hello"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", r.m, r.p, w.Code)
		}
	}
}

func TestDirectoryAPI_NotHosted(t *testing.T) {
	h := newDirectoryHarness(t, false)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	for _, r := range directoryRoutes {
		if w := h.do(r.m, r.p, `{"repo":"o/hello"}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503, got %d", r.m, r.p, w.Code)
		}
	}
}

func TestDirectoryAPI_Lifecycle(t *testing.T) {
	h := newDirectoryHarness(t, true)
	h.auth.On("IsAdmin", mock.Anything).Return(true)

	w := h.do("POST", "/api/v1/directory/repos", `{"repo":"https://github.com/o/hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var added directory.Repo
	json.Unmarshal(w.Body.Bytes(), &added)
	if added.Status != directory.StatusApproved || added.ID == 0 {
		t.Fatalf("added = %+v", added)
	}
	id := fmt.Sprint(added.ID)

	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/hello"}`); w.Code != http.StatusConflict {
		t.Errorf("add twice: %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad repo: %d", w.Code)
	}
	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/other"}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "no such repository") {
		t.Errorf("validation failure: %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/api/v1/directory/repos?status=approved", "")
	var list []directory.RepoView
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || len(list) != 1 || list[0].Name != "hello" || list[0].Stars != 7 {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/api/v1/directory/repos/"+id, "")
	var got struct {
		Repo   directory.RepoView `json:"repo"`
		Detail registry.DetailDoc `json:"detail"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != http.StatusOK || got.Repo.ID != added.ID || got.Detail.ReadmeHTML != "<p># Hello</p>" {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}

	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/reject", `{"reason":"not yet"}`); w.Code != http.StatusOK {
		t.Errorf("reject: %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/api/v1/directory/repos?status=rejected", "")
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].RejectReason != "not yet" {
		t.Errorf("rejected list: %s", w.Body.String())
	}
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/approve", ""); w.Code != http.StatusOK {
		t.Errorf("approve: %d", w.Code)
	}
	if raw, _ := h.dir.Service().Index(); !strings.Contains(string(raw), `"name": "hello"`) {
		t.Errorf("approve must regenerate the index: %s", raw)
	}

	h.src.missing = true
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/rebuild", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("rebuild failure: %d %s", w.Code, w.Body.String())
	}
	h.src.missing = false
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/rebuild", ""); w.Code != http.StatusOK {
		t.Errorf("rebuild: %d %s", w.Code, w.Body.String())
	}

	if w := h.do("DELETE", "/api/v1/directory/repos/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("delist: %d", w.Code)
	}
	for _, r := range directoryRoutes[2:] {
		if w := h.do(r.m, strings.Replace(r.p, "/1", "/"+id, 1), `{}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s after delist: expected 404, got %d", r.m, r.p, w.Code)
		}
	}
	if w := h.do("GET", "/api/v1/directory/repos/abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id: %d", w.Code)
	}
}

func TestPluginStatus_ReportsDirectoryHosted(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	dir := directory.New()
	dir.OnInit(h.db)
	h.ad.Directory = dir
	w := h.do("GET", "/api/v1/plugins/status", "")
	if !strings.Contains(w.Body.String(), `"directory_hosted":false`) {
		t.Errorf("not enabled: %s", w.Body.String())
	}
	h.db.Create(&plugin.PluginSetting{PluginName: "directory", Key: "enabled", Value: "true"})
	w = h.do("GET", "/api/v1/plugins/status", "")
	if !strings.Contains(w.Body.String(), `"directory_hosted":true`) {
		t.Errorf("enabled: %s", w.Body.String())
	}
}
```

`newPluginsHarness` (in `admin/plugins_test.go`) must expose the DB for the last test: add `db *gorm.DB` to `pluginsHarness` and set it in the constructor (`return &pluginsHarness{router: router, auth: a, inst: inst, srv: srv, ad: ad, db: db}`). Note `ad` there is a value (`admin.Admin`), so `h.ad.Directory = dir` works because the router handlers were bound to `ad`'s methods… **check**: if `admin.New` returns a value and the router captured method values of that copy, setting `h.ad.Directory` later has no effect. In that case change the harness to keep `ad := admin.New(...)` as a pointer (`ad` is already used as `ad.PluginStatus` — if `New` returns `Admin`, take `adp := &ad` and bind routes to `adp`, store `adp`). Add `"net/http/httptest"` to the imports of `directory_test.go`.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./admin/ -run 'Directory|DirectoryHosted' 2>&1 | head`
Expected: compile errors (`ad.ListDirectoryRepos` undefined, `Directory` field).

- [ ] **Step 4: Add the field, the status flag and the handlers**

`admin/admin.go`, next to `Installer`:

```go
	Directory     *directory.Plugin    // the directory this site hosts; nil when not wired
```

(import `"goblog/plugins/directory"`).

`plugin/installer/installer.go`, in `Status`:

```go
	DirectoryHosted bool `json:"directory_hosted"` // this site hosts a directory (set by admin, not the installer)
```

`admin/plugins.go`, `PluginStatus`:

```go
	st := inst.Status()
	st.DirectoryHosted = a.Directory != nil && a.Directory.Hosted()
	c.JSON(http.StatusOK, st)
```

`admin/directory.go`:

```go
package admin

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"goblog/plugins/directory"

	"github.com/gin-gonic/gin"
)

// requireDirectory checks admin auth and that this site hosts a directory.
// It writes the response and returns nil when the caller should stop.
func (a *Admin) requireDirectory(c *gin.Context) *directory.Service {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return nil
	}
	if a.Directory == nil || a.Directory.Service() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "this site does not host a plugin directory"})
		return nil
	}
	return a.Directory.Service()
}

// directoryStatus maps service errors to HTTP status codes; 0 means "not a
// client error".
func directoryStatus(err error) int {
	var ve *directory.ValidationError
	switch {
	case errors.Is(err, directory.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, directory.ErrBadRepo):
		return http.StatusBadRequest
	case errors.Is(err, directory.ErrAlreadyListed), errors.Is(err, directory.ErrUnderReview), errors.Is(err, directory.ErrNameTaken):
		return http.StatusConflict
	case errors.As(err, &ve):
		return http.StatusUnprocessableEntity
	case errors.Is(err, directory.ErrBusy), errors.Is(err, directory.ErrRateLimited):
		return http.StatusTooManyRequests
	}
	return 0
}

func writeDirectoryError(c *gin.Context, err error) {
	if code := directoryStatus(err); code != 0 {
		c.JSON(code, gin.H{"message": err.Error()})
		return
	}
	log.Printf("Directory API: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "internal error"})
}

func repoID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
		return 0, false
	}
	return uint(id), true
}

// ListDirectoryRepos returns submitted repositories, optionally filtered by ?status=.
func (a *Admin) ListDirectoryRepos(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	list, err := svc.List(strings.TrimSpace(c.Query("status")))
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

// GetDirectoryRepo returns one repository with its full detail document.
func (a *Admin) GetDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	v, d, err := svc.Get(id)
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"repo": v, "detail": d})
}

// AddDirectoryRepo validates a repository and lists it at once: POST {repo}.
func (a *Admin) AddDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	var req struct {
		Repo string `json:"repo"`
	}
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Repo) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "repo is required"})
		return
	}
	r, err := svc.Add(c.Request.Context(), req.Repo, a.Directory.Token())
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, r)
}

// ApproveDirectoryRepo lists a pending or rejected repository.
func (a *Admin) ApproveDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Approve(id); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "approved"})
}

// RejectDirectoryRepo keeps a repository off the directory: POST {reason}.
func (a *Admin) RejectDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	c.BindJSON(&req) // an empty body is a rejection without a reason
	if err := svc.Reject(id, strings.TrimSpace(req.Reason)); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "rejected"})
}

// RebuildDirectoryRepo re-validates a repository now.
func (a *Admin) RebuildDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Rebuild(c.Request.Context(), id, a.Directory.Token()); err != nil {
		if directoryStatus(err) == 0 {
			// A build failure is the repository's problem, not ours: report
			// it as unprocessable with the registry's message.
			c.JSON(http.StatusUnprocessableEntity, gin.H{"message": err.Error()})
			return
		}
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "rebuilt"})
}

// DelistDirectoryRepo forgets a repository.
func (a *Admin) DelistDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Delist(id); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "delisted"})
}
```

`Rebuild`'s validation failure comes back as the raw registry error (not a `ValidationError`) — hence the explicit 422 branch. If the "after delist" loop in the test gets 422 for `rebuild` instead of 404, check that `Service.Rebuild` looks the repo up before building (it does in Task 7).

- [ ] **Step 5: Run the admin tests**

Run: `go test ./admin/ -v -run 'Directory|DirectoryHosted|PluginAPI'`
Expected: PASS. The existing `TestPluginAPI_*` keep passing (the status JSON gained a field).

- [ ] **Step 6: Commit**

```bash
go vet ./... && git add admin plugin/installer plugins/directory && git commit -m "admin: directory curation API

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Admin UI — Directory tab and password settings

**Files:**
- Modify: `themes/default/templates/admin_plugins.html`, `themes/default/templates/admin_settings.html`

No Go tests cover templates; verify by running the app (Task 12 step 5). Keep the template's existing conventions: `esc()` for every string, delegated click listeners, `data-*` attributes carrying ids only.

- [ ] **Step 1: Password inputs in `admin_settings.html`**

In the plugin settings loop (around the `{{ if eq .Type "textarea" }}` branch, line ~65) add a branch before the final `{{ else }}`:

```html
                        {{ else if eq .Type "password" }}
                        <input type="password" id="{{ $pluginName }}.{{ .Key }}" name="{{ $pluginName }}.{{ .Key }}" value="{{ index $values .Key }}" class="form-control" autocomplete="off">
```

- [ ] **Step 2: Add the tab markup to `admin_plugins.html`**

After the Browse `<li>` in the tab list:

```html
        <li class="nav-item" id="tab-directory-item" hidden><button class="nav-link" id="tab-directory" data-bs-toggle="tab" data-bs-target="#pane-directory" type="button" role="tab">Directory</button></li>
```

After the Browse `tab-pane` div:

```html
        <div class="tab-pane fade" id="pane-directory" role="tabpanel">
            <p class="text-muted">This site hosts the plugin directory. Repositories submitted at <a href="/plugins/submit">/plugins/submit</a> wait here for approval; everything approved is published in <a href="/plugins/index.json">index.json</a>.</p>
            <form class="row g-2 mb-4" id="directory-add-form">
                <div class="col-md-9"><input type="text" id="directory-add-repo" class="form-control" placeholder="https://github.com/owner/goblog-plugin-name" required></div>
                <div class="col-md-3 d-grid"><button type="submit" class="btn btn-primary" id="directory-add-btn">Add repository</button></div>
                <div class="col-12 small text-danger" id="directory-add-error"></div>
            </form>
            <h2 class="h5">Pending review</h2>
            <div id="directory-pending" class="row g-3 mb-4"><div class="col-12 text-muted">Loading…</div></div>
            <h2 class="h5">Listed</h2>
            <table class="table align-middle">
                <thead><tr><th>Plugin</th><th>Repository</th><th>Version</th><th>Stars</th><th>Last build</th><th></th></tr></thead>
                <tbody id="directory-approved"><tr><td colspan="6" class="text-muted">Loading…</td></tr></tbody>
            </table>
            <h2 class="h5">Rejected</h2>
            <table class="table align-middle">
                <thead><tr><th>Repository</th><th>Reason</th><th>Decided</th><th></th></tr></thead>
                <tbody id="directory-rejected"><tr><td colspan="4" class="text-muted">None.</td></tr></tbody>
            </table>
        </div>
```

- [ ] **Step 3: Add the JS**

Inside the IIFE, before `loadStatus();` at the bottom, add:

```js
  // ---- Directory tab (only when this site hosts the directory) ----
  function directoryError(el, msg) { el.textContent = msg || ""; }
  function fmtDate(s) { return s ? esc(String(s).slice(0, 10)) : ""; }
  function hostsHTML(hosts) {
    var parts = (hosts || []).map(function (h) {
      var note = hostNote(h);
      return esc(h) + (note ? ' <span class="badge text-bg-warning">' + esc(note) + '</span>' : '');
    });
    return parts.length ? "Talks to: " + parts.join(", ") : "No network access";
  }
  function pendingCard(r) {
    var source = httpURL(r.source_url);
    return '<div class="col-md-6"><div class="card h-100" data-repo-id="' + esc(r.id) + '"><div class="card-body">' +
      '<h5 class="card-title mb-1">' + esc(r.display_name) + ' <small class="text-muted">' + esc(r.name) + ' v' + esc(r.version) + '</small></h5>' +
      '<p class="mb-1 small text-muted">by ' + esc(r.author) + ' · ' + esc(r.license) + ' · requires goblog ≥ ' + esc(r.min_goblog_version) + ' · submitted ' + fmtDate(r.submitted_at) + '</p>' +
      '<p class="small text-muted mb-1">' + hostsHTML(r.allowed_hosts) + '</p>' +
      (source ? '<a href="' + esc(source) + '" target="_blank" rel="noopener" class="me-2">' + esc(r.repo) + '</a>' : esc(r.repo)) +
      '<button type="button" class="btn btn-sm btn-link" data-action="readme" data-id="' + esc(r.id) + '">Show README</button>' +
      '<div class="plugin-readme border rounded p-2 mt-2" data-readme hidden></div>' +
      '</div><div class="card-footer d-flex justify-content-end gap-2">' +
      '<button type="button" class="btn btn-sm btn-outline-danger" data-action="reject" data-id="' + esc(r.id) + '">Reject</button>' +
      '<button type="button" class="btn btn-sm btn-success" data-action="approve" data-id="' + esc(r.id) + '">Approve</button>' +
      '</div><div class="small text-danger px-3 pb-2 row-error"></div></div></div>';
  }
  function approvedRow(r) {
    var source = httpURL(r.source_url);
    var build = r.last_error
      ? '<span class="text-danger" title="' + esc(r.last_error) + '">failed ' + fmtDate(r.last_attempt_at) + '</span><br><small class="text-muted">serving v' + esc(r.version) + ' from ' + fmtDate(r.built_at) + '</small>'
      : fmtDate(r.built_at);
    return '<tr data-repo-id="' + esc(r.id) + '">' +
      '<td><strong>' + esc(r.display_name) + '</strong><br><small class="text-muted">' + esc(r.name) + '</small></td>' +
      '<td>' + (source ? '<a href="' + esc(source) + '" target="_blank" rel="noopener">' + esc(r.repo) + '</a>' : esc(r.repo)) + '</td>' +
      '<td>v' + esc(r.version) + '</td><td>★ ' + esc(r.stars) + '</td><td>' + build + '</td>' +
      '<td class="text-end"><button type="button" class="btn btn-sm btn-outline-secondary me-1" data-action="rebuild" data-id="' + esc(r.id) + '">Rebuild</button>' +
      '<button type="button" class="btn btn-sm btn-outline-danger" data-action="delist" data-id="' + esc(r.id) + '">Delist</button>' +
      '<div class="small text-danger row-error"></div></td></tr>';
  }
  function rejectedRow(r) {
    return '<tr data-repo-id="' + esc(r.id) + '"><td>' + esc(r.repo) + '</td><td>' + esc(r.reject_reason) + '</td><td>' + fmtDate(r.decided_at) + '</td>' +
      '<td class="text-end"><button type="button" class="btn btn-sm btn-success me-1" data-action="approve" data-id="' + esc(r.id) + '">Approve</button>' +
      '<button type="button" class="btn btn-sm btn-outline-danger" data-action="delist" data-id="' + esc(r.id) + '">Delete</button>' +
      '<div class="small text-danger row-error"></div></td></tr>';
  }
  function renderDirectory(list) {
    var pending = list.filter(function (r) { return r.status === "pending"; });
    var approved = list.filter(function (r) { return r.status === "approved"; });
    var rejected = list.filter(function (r) { return r.status === "rejected"; });
    document.getElementById("directory-pending").innerHTML = pending.map(pendingCard).join("") || '<div class="col-12 text-muted">Nothing waiting.</div>';
    document.getElementById("directory-approved").innerHTML = approved.map(approvedRow).join("") || '<tr><td colspan="6" class="text-muted">Nothing listed yet — add a repository above.</td></tr>';
    document.getElementById("directory-rejected").innerHTML = rejected.map(rejectedRow).join("") || '<tr><td colspan="4" class="text-muted">None.</td></tr>';
    var badge = pending.length ? ' <span class="badge text-bg-warning">' + pending.length + '</span>' : '';
    document.getElementById("tab-directory").innerHTML = "Directory" + badge;
  }
  window.loadDirectory = function () {
    return api("GET", "/api/v1/directory/repos").then(renderDirectory).catch(function (e) { showError(e.message); });
  };
  function directoryAction(action, id, btn) {
    var container = btn.closest("tr, .card");
    var errEl = container ? container.querySelector(".row-error") : null;
    if (errEl) { errEl.textContent = ""; }
    if (action === "readme") {
      var box = container.querySelector("[data-readme]");
      if (!box.hidden) { box.hidden = true; btn.textContent = "Show README"; return; }
      api("GET", "/api/v1/directory/repos/" + encodeURIComponent(id)).then(function (data) {
        // readme_html is GitHub-rendered and sanitized, the same HTML the
        // public directory page shows; it is the one field inserted as-is.
        box.innerHTML = (data.detail && data.detail.readme_html) || "<em>No README.</em>";
        box.hidden = false; btn.textContent = "Hide README";
      }).catch(function (e) { if (errEl) { errEl.textContent = e.message; } });
      return;
    }
    var body;
    if (action === "reject") {
      var reason = prompt("Reason for rejecting (shown only here):", "");
      if (reason === null) { return; }
      body = { reason: reason };
    }
    if (action === "delist" && !confirm("Remove this repository from the directory? It can be submitted again later.")) { return; }
    btn.disabled = true;
    var label = btn.innerHTML;
    btn.innerHTML = '<span class="spinner-border spinner-border-sm"></span> ' + label;
    function restore() { btn.disabled = false; btn.innerHTML = label; }
    var p = action === "delist"
      ? api("DELETE", "/api/v1/directory/repos/" + encodeURIComponent(id))
      : api("POST", "/api/v1/directory/repos/" + encodeURIComponent(id) + "/" + action, body);
    p.then(function () { return loadDirectory().then(restore, restore); }, function (e) {
      restore();
      if (errEl) { errEl.textContent = e.message; } else { showError(e.message); }
    });
  }
  ["directory-pending", "directory-approved", "directory-rejected"].forEach(function (id) {
    document.getElementById(id).addEventListener("click", function (e) {
      var btn = e.target.closest("[data-action]");
      if (btn) { directoryAction(btn.dataset.action, btn.dataset.id, btn); }
    });
  });
  document.getElementById("directory-add-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var input = document.getElementById("directory-add-repo");
    var btn = document.getElementById("directory-add-btn");
    var errEl = document.getElementById("directory-add-error");
    directoryError(errEl, "");
    btn.disabled = true;
    var label = btn.textContent;
    btn.innerHTML = '<span class="spinner-border spinner-border-sm"></span> Checking…';
    api("POST", "/api/v1/directory/repos", { repo: input.value.trim() }).then(function () {
      input.value = "";
      return loadDirectory();
    }).catch(function (e) { directoryError(errEl, e.message); }).then(function () { btn.disabled = false; btn.textContent = label; });
  });
```

And in `loadStatus`, after `renderInstalled(st);`:

```js
      document.getElementById("tab-directory-item").hidden = !st.directory_hosted;
      if (st.directory_hosted) { loadDirectory(); }
```

- [ ] **Step 4: Sanity-check the template parses**

Run: `go test ./admin/ ./blog/ 2>&1 | tail -3` (templates are parsed by the blog/admin test harnesses that load `themes/default`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add themes/default/templates && git commit -m "admin: Directory tab and password setting inputs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: Wiring, docs, contract, run it

**Files:**
- Modify: `goblog.go`, `README.md`, `docs/superpowers/specs/2026-09-20-db-backed-plugin-directory-design.md`
- Create: `docs/PLUGIN_CONTRACT.md` (from `~/dev/goblog-plugins/docs/CONTRACT.md`)

- [ ] **Step 1: Wire main**

In `goblog.go` after `_admin.Installer = pluginInstaller`:

```go
	_admin.Directory = dir
```

Next to the `/api/v1/plugins/*` routes:

```go
	router.GET("/api/v1/directory/repos", goblog._admin.ListDirectoryRepos)
	router.POST("/api/v1/directory/repos", goblog._admin.AddDirectoryRepo)
	router.GET("/api/v1/directory/repos/:id", goblog._admin.GetDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/approve", goblog._admin.ApproveDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/reject", goblog._admin.RejectDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/rebuild", goblog._admin.RebuildDirectoryRepo)
	router.DELETE("/api/v1/directory/repos/:id", goblog._admin.DelistDirectoryRepo)
```

Run: `go build ./... && go vet ./... && go test ./...` — expected PASS.

- [ ] **Step 2: Contract doc**

```bash
cp ~/dev/goblog-plugins/docs/CONTRACT.md docs/PLUGIN_CONTRACT.md
```

Edit the first paragraph to:

> The directory at [goblog.live/plugins](https://goblog.live/plugins) lists plugins that have been submitted at [goblog.live/plugins/submit](https://goblog.live/plugins/submit) and approved by a maintainer. A plugin is a GitHub repository whose releases each carry a compiled WebAssembly module; each GitHub release is a version. Submission checks the repository right away (the checks below) and queues it; once approved, the directory re-checks it every few hours and picks up new releases on its own.

Replace `internal/registry/manifest.go` in the license bullet with `plugins/directory/registry/manifest.go`. Replace any sentence about `registry.yaml`, pull requests or CI with the submission page. Keep everything else (manifest, module, releases, workflow).

- [ ] **Step 3: README**

Replace the `### Plugin directory` section with:

```markdown
### Plugin directory
[goblog.live/plugins](https://goblog.live/plugins) lists published plugins; `https://goblog.live/plugins/index.json` is the same list as JSON (name, version, author, license, `download_url`, `sha256`, `min_goblog_version`, `runtime`, `allowed_hosts`) and `/plugins/<name>.json` carries one plugin's README, changelog and release history. Plugins are individual GitHub repositories with releases — see [docs/PLUGIN_CONTRACT.md](docs/PLUGIN_CONTRACT.md). To publish one, paste its URL at [goblog.live/plugins/submit](https://goblog.live/plugins/submit): it is validated on the spot (latest release, manifest, `plugin.wasm` loads and its name/version match) and listed once a maintainer approves it.

The directory is the built-in `directory` plugin, so any goblog can host one: turn it on under **Admin → Settings → Plugin Directory** (`enabled` = `true`). Submissions are stored in the site's database and reviewed under **Admin → Plugins → Directory**, where you can also add repositories yourself, rebuild an entry or delist it. Listed plugins are re-checked every `refresh_minutes` (default 360) for new releases and star counts. The GitHub API allows 60 anonymous requests per hour; set `github_token` (any token, no scopes needed) to raise that to 5000 if you list more than a handful of plugins.
```

Also update the sentence at line ~256 (`This is what the plugin directory registry runs on every submission.`) to `This is the same check goblog.live runs on every submission to the plugin directory.`

- [ ] **Step 4: Note the deviations in the spec**

In the spec's section 1 table for `directory_builds`, replace the row listing the `IndexEntry` fields with `| `doc` | the detail document as JSON (index fields + README/changelog HTML + releases) |` and add `| `version`, `stars` | copied out of `doc` for the admin list and the unchanged-release check |`. In section 5, replace "`directory.Fetcher` and the `Entry`/`Detail` wire types move" with "`directory.Fetcher` moves (the wire types stay in `plugins/directory`)". In section 3's submission step 4 replace the rate-limit sentence with "rate limit: 5 attempts per hour per client IP, counted in memory (every attempt, pass or fail)".

- [ ] **Step 5: Run it for real**

```bash
cd ~/dev/goblog && go build -o goblog . && ENABLE_WASM_PLUGINS=true ./goblog
```

In the browser (or `curl`): log in as admin, Admin → Settings → Plugin Directory → `enabled` = `true` (save), reload Admin → Plugins → the **Directory** tab appears → Add `goblogplatform/goblog-plugin-hello` → it appears under Listed. Then `curl -s localhost:7000/plugins/index.json` shows `hello`; `/plugins`, `/plugins/hello`, `/plugins/hello.json` render; `/plugins/submit` shows the form; submitting `goblogplatform/goblog-plugin-scholar` from a private window lands it under Pending review; Approve → both in the index. Note anything that doesn't behave as described and fix it before the PR.

- [ ] **Step 6: Commit and open the PR**

```bash
git add goblog.go README.md docs && git commit -m "Wire the directory admin API; document the DB-backed directory

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/db-backed-plugin-directory
gh pr create --title "Database-backed plugin directory" --body "$(cat <<'BODY'
## Summary
- The `directory` plugin is now the registry: repos are submitted at `/plugins/submit`, validated in-process (`wasm.LoadBytes`) and queued; admins approve/reject/add/rebuild/delist under Admin → Plugins → Directory; `index.json`, `/plugins/<name>` and `/plugins/<name>.json` are served from the DB.
- `plugins/directory/registry` ports the registry repo's validation/build (GitHub over `net/http`, no new deps); the installer's `Fetcher` moves to `plugin/installer`.
- Settings: `index_url` removed; `refresh_minutes` default 360 (min 15); optional `github_token`.
- Spec: `docs/superpowers/specs/2026-09-20-db-backed-plugin-directory-design.md`; contract doc moved to `docs/PLUGIN_CONTRACT.md`.

Follow-ups: goblog-site-theme template, `goblogplatform/plugins` retirement, `v0.4.0` release + iac bump.

## Test plan
- [ ] `go test ./...`
- [ ] Local run: enable directory, add hello via admin, submit scholar via the public form, approve, install from another goblog's Admin → Plugins.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

---

### Task 13: `goblog-site-theme` — admin templates

**Files (repo `~/dev/goblog-site-theme`):**
- Modify: `templates/admin_plugins.html`, `templates/admin_settings.html`

The theme's `admin_plugins.html` has diverged slightly from `themes/default` (a stricter IPv6 `hostNote` regex and health badges in `runtimeBadge`) — do **not** overwrite it; apply Task 11's three edits (tab `<li>`, tab pane, JS block + the two `loadStatus` lines) and the password branch by hand.

- [ ] **Step 1: Branch and apply**

```bash
cd ~/dev/goblog-site-theme && git checkout main && git pull && git checkout -b feat/directory-admin-tab
```

Apply the edits from Task 11 steps 1–3 to the theme's copies. Verify with `diff <(sed -n '/Directory tab/,/directory-add-form/p' ~/dev/goblog/themes/default/templates/admin_plugins.html) <(sed -n '/Directory tab/,/directory-add-form/p' templates/admin_plugins.html)` — expected: no output.

- [ ] **Step 2: Commit and PR**

```bash
git add templates && git commit -m "Admin: Directory tab and password setting inputs

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/directory-admin-tab
gh pr create --title "Admin: Directory tab" --body "Mirrors goblog's themes/default admin_plugins.html (Directory tab for sites hosting the plugin directory) and admin_settings.html (password inputs). Pairs with goblogplatform/goblog PR 'Database-backed plugin directory'.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

---

### Task 14: Retire `goblogplatform/plugins`

**Files (repo `~/dev/goblog-plugins`):**
- Delete: `registry.yaml`, `cmd/`, `internal/`, `dist/`, `go.mod`, `go.sum`, `renovate.json`, `.github/workflows/publish.yml`, `.github/workflows/submit.yml`, `.github/workflows/validate.yml`, `.github/ISSUE_TEMPLATE/submit-plugin.yml`, `docs/CONTRACT.md`
- Rewrite: `README.md`

Do this only after the goblog PR is merged, released and goblog.live is seeded (Task 12's PR + the release + iac bump + Add hello/scholar in admin), so `https://www.goblog.live/plugins/index.json` never goes empty for installers while Pages is still the URL some old `index_url` settings point at.

- [ ] **Step 1: Branch and remove**

```bash
cd ~/dev/goblog-plugins && git checkout main && git pull && git checkout -b retire
git rm -r registry.yaml cmd internal dist go.mod go.sum renovate.json .github docs
```

- [ ] **Step 2: README**

```markdown
# goblog plugins

This repository used to hold the curated list behind [goblog.live/plugins](https://goblog.live/plugins) and the CI that built its index. The directory is now run by goblog.live itself:

- **Publish a plugin:** paste your repository URL at [goblog.live/plugins/submit](https://goblog.live/plugins/submit). It is validated immediately and listed once approved.
- **What a plugin repository must contain:** [docs/PLUGIN_CONTRACT.md](https://github.com/goblogplatform/goblog/blob/main/docs/PLUGIN_CONTRACT.md) in the goblog repository.
- **Examples:** [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello) (smallest complete plugin) and [goblog-plugin-scholar](https://github.com/goblogplatform/goblog-plugin-scholar).

The machine-readable index is `https://www.goblog.live/plugins/index.json`; the GitHub Pages copy that used to live under `goblogplatform.github.io/plugins` is gone.
```

- [ ] **Step 3: Commit, PR, then manual steps**

```bash
git add -A && git commit -m "Retire the GitHub-driven registry; the directory lives in goblog.live

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin retire
gh pr create --title "Retire the registry" --body "goblog.live's database is the registry now (goblogplatform/goblog: database-backed plugin directory). Removes the list, the build tool, the workflows and the issue form; the README points at goblog.live/plugins/submit and the contract doc in goblog.

After merging: Settings → Pages → disable; then archive the repository.

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
```

After the user merges: they disable GitHub Pages and archive the repo (settings, not code).

---

## Self-review

- **Spec coverage:** §1 data model → Task 5; §2 builder → Tasks 1–4; §3 plugin settings/cache/job/pages/submission → Tasks 7–8; §4 admin → Tasks 10–11; §5 installer → Task 9; §6 retirement/deploy → Tasks 12–14 (release + iac bump + seeding are the user's manual steps, listed in Task 14's precondition); §7 tests → each task. Out-of-scope items untouched.
- **Placeholders:** none; every code step carries the code.
- **Type consistency:** `registry.IndexEntry/DetailDoc/ReleaseDoc` (Task 4) aliased as `directory.Entry/Detail/Release` (Task 5); `Service` method names in Tasks 7, 8, 10 match; `FakeValidator`/`Sum` from Task 3 are used in Tasks 4, 7, 10; `Plugin.SetSource/SetValidator` added in Task 10 before the admin test uses them; `installer.Status.DirectoryHosted` set in Task 10 and read by Task 11's JS as `directory_hosted`.
