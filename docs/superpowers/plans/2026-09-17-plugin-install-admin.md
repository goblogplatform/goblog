# Admin Plugin Install (goblog side) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an admin browse/search the plugin directory and install, update or uninstall dynamic plugins with one click, live — plus stars and a "Submit your plugin" box on the public directory page.

**Architecture:** The plugin registry gains per-plugin lifecycle (entries with their own stop channel, `RegisterDynamic`, `Unregister`, `InitPlugin`, `Init` that continues past failures). A new `plugin/installer` package does download → sha256 → Yaegi load → identity check → atomic write → register, with typed errors. `admin` exposes a JSON API and an `/admin/plugins` page (vanilla JS, same pattern as the other admin pages). The directory client is `plugins/directory.Fetcher`, reused with its own instance and the `plugin_directory_url` site setting.

**Tech Stack:** Go 1.25, gin, gorm/sqlite, Yaegi, `html/template`, `net/http/httptest`. No new module dependencies (semver comparison is a 20-line helper).

**Spec:** `docs/superpowers/specs/2026-09-17-plugin-install-admin-design.md` (sections 1–3 and the goblog half of 4). The registry-side `stars`/submission workflow (spec §4) and iac (§5) are separate plans.

## Global Constraints

- Module path `goblog`; no new `go.mod` dependencies.
- Only dynamic plugins are installable/updatable/uninstallable; compiled-in plugins are listed but never touched.
- Preconditions: `ENABLE_DYNAMIC_PLUGINS=true` (→ `Installer.Enabled`) and a writable `plugins/dynamic/`; Browse/Status must work without them, Install/Update/Uninstall return `ErrDynamicDisabled`.
- sha256 from the index must match the download or nothing is written. Download over HTTPS only (loopback hosts allowed for tests), capped at 1 MB.
- A plugin is registered only if `LoadDynamicPluginBytes` succeeds and `Name()` == index `name` and `Version()` == index `version`.
- `min_goblog_version` check is skipped when the running version is `development`, `latest`, empty, or otherwise unparseable.
- Site setting `plugin_directory_url`, type `text`, default `https://www.goblog.live/plugins/index.json` (constant `installer.DefaultIndexURL`), seeded in `tools/migrate.go` `seedDefaultSettings`.
- API paths (admin only, JSON): `GET /api/v1/plugins/status`, `GET /api/v1/plugins/directory?q=&sort=stars|name|newest`, `POST /api/v1/plugins/install {name}`, `POST /api/v1/plugins/update {name}`, `DELETE /api/v1/plugins/:name`, `POST /api/v1/plugins/refresh`. Non-admin → 401 `"Not Authorized"` (existing convention). Typed installer errors → 4xx `{"message": "..."}`; other errors → 500 + log.
- Page: `GET /admin/plugins` → `admin_plugins.html`; nav link between "Post Types" and "Settings".
- Settings rows are kept on update, deleted on uninstall.
- `Init` returns `errors.Join` of per-plugin failures and continues (#569); `main` logs it.
- Commit messages: imperative, `(#553)` suffix, trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Branch `feat/553-plugin-install`; never push to `main`; never merge. `git branch --show-current` before every commit.
- Verify with what CI runs: `go build -v . && go vet ./... && go test -race goblog/...` (two pre-existing `go vet` lock-copy warnings in `blog/` are known).

---

## File structure

| File | Responsibility |
|---|---|
| `plugin/registry.go` | entries with stop channels; `Register`, `RegisterDynamic`, `Unregister`, `Init` (joined errors), `InitPlugin`, `StartScheduledJobs` (idempotent), `Stop`, `DeleteSettings`, `Dynamic` |
| `plugin/loader.go` | `LoadDynamicPlugin(path)`, `LoadDynamicPluginBytes(src)` exported; `LoadDynamicPlugins` uses `RegisterDynamic` |
| `plugin/registry_test.go`, `plugin/loader_test.go` | lifecycle tests |
| `plugin/installer/installer.go` | `Installer`, typed errors, `Install`/`Update`/`Uninstall`/`Status`/`Refresh` |
| `plugin/installer/version.go` (+`_test`) | `compatible(running, min string) bool`, `newer(a, b string) bool` |
| `plugin/installer/installer_test.go` | httptest-driven tests |
| `blog/blog.go` | `SettingValue(key, def string) string` |
| `tools/migrate.go` | seed `plugin_directory_url` |
| `admin/plugins.go` (+`admin/plugins_test.go`) | API handlers + `AdminPlugins` page handler |
| `admin/admin.go` | `Installer *installer.Installer` field |
| `goblog.go` | build the installer, routes, log `Init` errors |
| `themes/default/templates/admin_plugins.html`, `admin_nav.html` | the page |
| `plugins/directory/index.go`, `templates/listing.html`, `directory.go` | `Stars`, sort by stars, Submit box |
| `README.md` | docs |

---

### Task 1: Registry lifecycle (register/unregister, per-plugin jobs, joined Init errors)

**Files:**
- Modify: `plugin/registry.go` (struct, `NewRegistry`, `Register`, `Init`, `StartScheduledJobs`, `Stop`, plus new methods)
- Modify: `plugin/loader.go` (export loader; use `RegisterDynamic`)
- Modify: `goblog.go:308-312` and `:147-149` (log `Init` errors)
- Test: `plugin/registry_test.go`, `plugin/loader_test.go`

**Interfaces:**
- Produces (package `plugin`):
  - `func (r *Registry) RegisterDynamic(p Plugin, path string) error` — error if a plugin with that `Name()` is registered.
  - `func (r *Registry) Unregister(name string) error` — closes its jobs' stop channel, removes it; settings kept.
  - `func (r *Registry) InitPlugin(name string) error` — seed settings + `OnInit` + start jobs for one plugin.
  - `func (r *Registry) Init() error` — all plugins, continues past failures, `errors.Join`.
  - `func (r *Registry) DeleteSettings(name string)`.
  - `type DynamicInfo struct{ Name, DisplayName, Version, Path string }`; `func (r *Registry) Dynamic() []DynamicInfo`.
  - `func LoadDynamicPlugin(path string) (Plugin, error)`; `func LoadDynamicPluginBytes(src []byte) (Plugin, error)`.
  - `StartScheduledJobs()` is idempotent per plugin (calling it twice does not double-start).

- [ ] **Step 1: Write the failing tests**

Append to `plugin/registry_test.go` (existing imports: `goblog/plugin`, `net/http`, `net/http/httptest`, `strings`, `testing`, gin, sqlite, gorm; add `errors`, `sync/atomic`, `time`):

```go
// jobPlugin counts how often its 10ms job runs.
type jobPlugin struct {
	plugin.BasePlugin
	name string
	runs atomic.Int32
}

func (p *jobPlugin) Name() string        { return p.name }
func (p *jobPlugin) DisplayName() string { return "Job " + p.name }
func (p *jobPlugin) Version() string     { return "1.0.0" }
func (p *jobPlugin) ScheduledJobs() []plugin.ScheduledJob {
	return []plugin.ScheduledJob{{Name: "tick", Interval: 10 * time.Millisecond, Run: func(*gorm.DB, map[string]string) error {
		p.runs.Add(1)
		return nil
	}}}
}

// failInitPlugin fails OnInit.
type failInitPlugin struct{ plugin.BasePlugin }

func (failInitPlugin) Name() string             { return "failing" }
func (failInitPlugin) DisplayName() string      { return "Failing" }
func (failInitPlugin) Version() string          { return "0.0.1" }
func (failInitPlugin) OnInit(*gorm.DB) error    { return errors.New("boom") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestRegistry(t *testing.T) *plugin.Registry {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	return plugin.NewRegistry(db)
}

func hasPlugin(reg *plugin.Registry, name string) bool {
	for _, p := range reg.Plugins() {
		if p.Name() == name {
			return true
		}
	}
	return false
}

func TestUnregisterStopsOnlyThatPluginsJobs(t *testing.T) {
	reg := newTestRegistry(t)
	a, b := &jobPlugin{name: "a"}, &jobPlugin{name: "b"}
	reg.Register(a)
	if err := reg.RegisterDynamic(b, "/tmp/b.go"); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDynamic(&jobPlugin{name: "b"}, "/tmp/b2.go"); err == nil {
		t.Error("registering a second plugin named b should fail")
	}
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	reg.StartScheduledJobs()
	reg.StartScheduledJobs() // idempotent
	waitFor(t, "both jobs to run", func() bool { return a.runs.Load() > 0 && b.runs.Load() > 0 })

	dyn := reg.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "b" || dyn[0].Path != "/tmp/b.go" || dyn[0].DisplayName != "Job b" {
		t.Errorf("Dynamic() = %+v", dyn)
	}

	if err := reg.Unregister("b"); err != nil {
		t.Fatal(err)
	}
	if hasPlugin(reg, "b") || len(reg.Dynamic()) != 0 {
		t.Error("b should be gone after Unregister")
	}
	time.Sleep(30 * time.Millisecond)
	n := b.runs.Load()
	time.Sleep(50 * time.Millisecond)
	if b.runs.Load() != n {
		t.Error("b's job kept running after Unregister")
	}
	an := a.runs.Load()
	waitFor(t, "a's job to keep running", func() bool { return a.runs.Load() > an })

	if err := reg.Unregister("nope"); err == nil {
		t.Error("unregistering an unknown plugin should fail")
	}
	reg.Stop()
	time.Sleep(30 * time.Millisecond)
	an = a.runs.Load()
	time.Sleep(50 * time.Millisecond)
	if a.runs.Load() != an {
		t.Error("a's job kept running after Stop")
	}
}

func TestInitContinuesPastFailingPlugin(t *testing.T) {
	reg := newTestRegistry(t)
	reg.Register(failInitPlugin{})
	reg.Register(&testPlugin{})
	err := reg.Init()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the failing plugin's error, got %v", err)
	}
	for _, g := range reg.GetAllSettings() {
		if g.PluginName == "test" && g.CurrentValues["api_key"] == "default123" {
			return
		}
	}
	t.Error("the plugin registered after the failing one should still have its settings seeded")
}

func TestInitPluginAndDeleteSettings(t *testing.T) {
	reg := newTestRegistry(t)
	reg.Register(&testPlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	reg.StartScheduledJobs()

	late := &jobPlugin{name: "late"}
	if err := reg.RegisterDynamic(late, "late.go"); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitPlugin("late"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "late plugin's job", func() bool { return late.runs.Load() > 0 })
	if err := reg.InitPlugin("nope"); err == nil {
		t.Error("InitPlugin on an unknown plugin should fail")
	}

	reg.DeleteSettings("test")
	for _, g := range reg.GetAllSettings() {
		if g.PluginName == "test" && len(g.CurrentValues) != 0 {
			t.Errorf("settings for test should be deleted, got %v", g.CurrentValues)
		}
	}
	reg.Stop()
}
```

Append to `plugin/loader_test.go`:

```go
func TestLoadDynamicPluginBytes(t *testing.T) {
	src, err := os.ReadFile("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatal(err)
	}
	p, err := plugin.LoadDynamicPluginBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "hello" || p.Version() != "1.0.0" {
		t.Errorf("loaded %q %q", p.Name(), p.Version())
	}
	if _, err := plugin.LoadDynamicPluginBytes([]byte("package main\nfunc NewPlugin() int { return 1 }\n")); err == nil {
		t.Error("NewPlugin returning a non-plugin should fail")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./plugin/ -run 'TestUnregister|TestInitContinues|TestInitPlugin|TestLoadDynamicPluginBytes' -v`
Expected: FAIL to compile — `undefined: reg.RegisterDynamic`, `plugin.LoadDynamicPluginBytes`, etc.

- [ ] **Step 3: Implement the registry changes**

In `plugin/registry.go`, replace the struct, constructor, `Register`, `Plugins`, `Init`, `StartScheduledJobs`, `Stop` with:

```go
// entry is one registered plugin with its lifecycle state.
type entry struct {
	plugin  Plugin
	path    string        // source file for dynamic plugins; "" for compiled-in
	stop    chan struct{} // closed by Unregister/Stop; ends this plugin's jobs
	stopped bool
	started bool // jobs running
}

// DynamicInfo describes a plugin loaded from a file at runtime.
type DynamicInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
	Path        string `json:"path"`
}

// Registry manages all registered plugins.
type Registry struct {
	entries []*entry
	plugins []Plugin // derived from entries; rebuilt on every change
	db      *gorm.DB
	mu      sync.RWMutex
}

// NewRegistry creates a plugin registry.
func NewRegistry(db *gorm.DB) *Registry {
	return &Registry{db: db}
}

// UpdateDb updates the database reference (used after wizard setup).
func (r *Registry) UpdateDb(db *gorm.DB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
}

// Register adds a compiled-in plugin to the registry.
func (r *Registry) Register(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(p, "")
}

// RegisterDynamic adds a plugin loaded from path at runtime. It fails when a
// plugin with the same Name() is already registered, since names key
// settings and pages.
func (r *Registry) RegisterDynamic(p Plugin, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findLocked(p.Name()) != nil {
		return fmt.Errorf("a plugin named %q is already registered", p.Name())
	}
	r.addLocked(p, path)
	return nil
}

func (r *Registry) addLocked(p Plugin, path string) {
	r.entries = append(r.entries, &entry{plugin: p, path: path, stop: make(chan struct{})})
	r.rebuildLocked()
	log.Printf("Plugin registered: %s v%s", p.DisplayName(), p.Version())
}

// Unregister removes a plugin and stops its scheduled jobs. Its settings are
// kept so a reinstall or update keeps the operator's configuration; use
// DeleteSettings to remove them. Yaegi cannot unload code, so a dynamic
// plugin's interpreter stays in memory until restart.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.plugin.Name() != name {
			continue
		}
		e.closeStopLocked()
		r.entries = append(append([]*entry(nil), r.entries[:i]...), r.entries[i+1:]...)
		r.rebuildLocked()
		log.Printf("Plugin unregistered: %s", name)
		return nil
	}
	return fmt.Errorf("plugin %q is not registered", name)
}

func (e *entry) closeStopLocked() {
	if !e.stopped {
		e.stopped = true
		close(e.stop)
	}
}

func (r *Registry) findLocked(name string) *entry {
	for _, e := range r.entries {
		if e.plugin.Name() == name {
			return e
		}
	}
	return nil
}

// rebuildLocked refreshes the derived plugins slice; existing readers keep
// iterating their own copy.
func (r *Registry) rebuildLocked() {
	plugins := make([]Plugin, len(r.entries))
	for i, e := range r.entries {
		plugins[i] = e.plugin
	}
	r.plugins = plugins
}

// Plugins returns the list of registered plugins.
func (r *Registry) Plugins() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Plugin, len(r.plugins))
	copy(result, r.plugins)
	return result
}

// Dynamic lists the plugins that were loaded from files at runtime.
func (r *Registry) Dynamic() []DynamicInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []DynamicInfo
	for _, e := range r.entries {
		if e.path != "" {
			out = append(out, DynamicInfo{Name: e.plugin.Name(), DisplayName: e.plugin.DisplayName(), Version: e.plugin.Version(), Path: e.path})
		}
	}
	return out
}

// Init seeds plugin settings and calls OnInit for every plugin. A plugin
// that fails does not stop the others; the failures are returned joined.
func (r *Registry) Init() error {
	r.mu.RLock()
	entries := append([]*entry(nil), r.entries...)
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return nil
	}
	// Create the plugin_settings table if it doesn't exist
	db.AutoMigrate(&PluginSetting{})
	var errs []error
	for _, e := range entries {
		if err := initPlugin(db, e.plugin); err != nil {
			log.Printf("Plugin %s init error: %v", e.plugin.Name(), err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func initPlugin(db *gorm.DB, p Plugin) error {
	for _, s := range p.Settings() {
		setting := PluginSetting{PluginName: p.Name(), Key: s.Key, Value: s.DefaultValue}
		db.Where("plugin_name = ? AND key = ?", p.Name(), s.Key).FirstOrCreate(&setting)
	}
	if err := p.OnInit(db); err != nil {
		return fmt.Errorf("plugin %s: %w", p.Name(), err)
	}
	return nil
}

// InitPlugin seeds settings, runs OnInit and starts the scheduled jobs of one
// plugin — what a hot install needs after RegisterDynamic.
func (r *Registry) InitPlugin(name string) error {
	r.mu.RLock()
	e := r.findLocked(name)
	db := r.db
	r.mu.RUnlock()
	if e == nil {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	if db == nil {
		return errors.New("database is not ready")
	}
	db.AutoMigrate(&PluginSetting{})
	if err := initPlugin(db, e.plugin); err != nil {
		return err
	}
	r.startJobs(e)
	return nil
}

// StartScheduledJobs launches goroutines for all plugin scheduled jobs.
// Plugins whose jobs are already running are left alone.
func (r *Registry) StartScheduledJobs() {
	r.mu.RLock()
	entries := append([]*entry(nil), r.entries...)
	r.mu.RUnlock()
	for _, e := range entries {
		r.startJobs(e)
	}
}

func (r *Registry) startJobs(e *entry) {
	r.mu.Lock()
	if e.started || e.stopped {
		r.mu.Unlock()
		return
	}
	e.started = true
	r.mu.Unlock()
	for _, job := range e.plugin.ScheduledJobs() {
		go func(p Plugin, job ScheduledJob, stop <-chan struct{}) {
			ticker := time.NewTicker(job.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.mu.RLock()
					db := r.db
					r.mu.RUnlock()
					if db == nil {
						continue
					}
					settings := r.getPluginSettings(p.Name())
					if err := job.Run(db, settings); err != nil {
						log.Printf("Plugin %s job %s error: %v", p.Name(), job.Name, err)
					}
				case <-stop:
					return
				}
			}
		}(e.plugin, job, e.stop)
	}
}

// Stop gracefully shuts down every plugin's scheduled jobs.
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		e.closeStopLocked()
	}
}

// DeleteSettings removes a plugin's stored settings (uninstall).
func (r *Registry) DeleteSettings(name string) {
	r.mu.RLock()
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.Where("plugin_name = ?", name).Delete(&PluginSetting{})
}
```

Add `"errors"` and `"fmt"` to the imports. Every other method that loops `for _, p := range r.plugins` is unchanged (the derived slice is maintained by `rebuildLocked`).

In `plugin/loader.go`: rename `loadPlugin` to `LoadDynamicPlugin` and split out the bytes version; `LoadDynamicPlugins` uses `RegisterDynamic`:

```go
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		p, err := LoadDynamicPlugin(path)
		if err != nil {
			log.Printf("Warning: failed to load plugin %s: %v", entry.Name(), err)
			continue
		}
		if err := registry.RegisterDynamic(p, path); err != nil {
			log.Printf("Warning: skipping plugin %s: %v", entry.Name(), err)
		}
	}
}

// LoadDynamicPlugin loads one plugin source file through the interpreter.
func LoadDynamicPlugin(path string) (Plugin, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := LoadDynamicPluginBytes(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// LoadDynamicPluginBytes interprets plugin source and calls its NewPlugin().
// The source runs as Go code inside this process; only load what you trust.
func LoadDynamicPluginBytes(src []byte) (Plugin, error) {
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		return nil, err
	}
	if err := i.Use(Symbols); err != nil {
		return nil, err
	}
	if _, err := i.Eval(string(src)); err != nil {
		return nil, err
	}
	v, err := i.Eval("NewPlugin()")
	if err != nil {
		return nil, err
	}
	p, ok := v.Interface().(Plugin)
	if !ok {
		return nil, fmt.Errorf("NewPlugin() did not return a plugin.Plugin")
	}
	return p, nil
}
```

`plugin/validate.go` calls `loadPlugin(path)` — change it to `LoadDynamicPlugin(path)`.

In `goblog.go`, both `registry.Init()` / `g._registry.Init()` call sites become:

```go
		if err := registry.Init(); err != nil {
			log.Printf("Plugin init errors: %v", err)
		}
```
(and `g._registry` for the wizard path).

- [ ] **Step 4: Run the whole plugin package + build everything**

Run: `go build ./... && go vet ./plugin/ && go test -race ./plugin/ ./plugins/... ./blog/ . -count=1`
Expected: PASS (existing loader/validate tests included).

- [ ] **Step 5: Commit**

```bash
git branch --show-current   # feat/553-plugin-install
git add plugin/ goblog.go
git commit -m "Give the plugin registry per-plugin lifecycle: unregister, hot init, joined Init errors (#553)

Init no longer stops at the first failing OnInit (#569).

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `plugin/installer` — version helpers, Status, Install, Update, Uninstall

**Files:**
- Create: `plugin/installer/version.go`, `plugin/installer/installer.go`
- Test: `plugin/installer/version_test.go`, `plugin/installer/installer_test.go`

**Interfaces:**
- Consumes: Task 1's registry methods; `directory.Fetcher` (`Ensure(url)`, `Refresh(url)`, `Index()`, `Entry(name)`, `FetchedAt()`), `directory.Entry`.
- Produces (package `installer`):
  - `const DefaultIndexURL = "https://www.goblog.live/plugins/index.json"`
  - errors: `ErrDynamicDisabled, ErrIncompatible, ErrChecksum, ErrNotDynamic, ErrAlreadyInstalled, ErrNotInstalled, ErrNotFound, ErrLoad, ErrDirectoryUnavailable`
  - `type Installer struct{ Dir string; Registry *plugin.Registry; Directory *directory.Fetcher; Version string; Client *http.Client; Enabled bool; IndexURL func() string }`
  - `type Installed struct`, `type Available struct`, `type Status struct`, `type Result struct` (json tags below)
  - `func (i *Installer) Status() Status`; `Refresh() error`; `Install(ctx, name) (Result, error)`; `Update(ctx, name) (Result, error)`; `Uninstall(name) error`
  - `func compatible(running, min string) bool`; `func newer(candidate, current string) bool`

- [ ] **Step 1: Write the failing tests**

`plugin/installer/version_test.go`:

```go
package installer

import "testing"

func TestCompatible(t *testing.T) {
	cases := []struct {
		running, min string
		want         bool
	}{
		{"v0.2.7", "0.2.6", true},
		{"v0.2.7", "0.2.7", true},
		{"v0.2.7", "0.2.8", false},
		{"v0.2.7", "1.0.0", false},
		{"v1.0.0", "0.9.9", true},
		{"development", "9.9.9", true},
		{"latest", "9.9.9", true},
		{"", "9.9.9", true},
		{"v0.2.7", "", true},
		{"v0.2.7", "garbage", true},
	}
	for _, c := range cases {
		if got := compatible(c.running, c.min); got != c.want {
			t.Errorf("compatible(%q, %q) = %v, want %v", c.running, c.min, got, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"1.1.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.1.0", false},
		{"2.0.0", "1.9.9", true},
		{"v1.0.1", "1.0.0", true},
		{"garbage", "1.0.0", false},
		{"1.0.0", "garbage", false},
	}
	for _, c := range cases {
		if got := newer(c.candidate, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}
```

`plugin/installer/installer_test.go`:

```go
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"goblog/plugin"
	"goblog/plugins/directory"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fixture serves an index with the hello plugin (v1.0.0), a v1.1.0 variant,
// a plugin whose Name() disagrees with the index, and a compiled-in entry.
type fixture struct {
	srv        *httptest.Server
	helloV1    []byte
	helloV2    []byte
	badName    []byte
	entries    []map[string]any
	checksumOK atomic.Bool // when false, the index carries a wrong sha256 for hello
	hits       atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	src, err := os.ReadFile("../../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{helloV1: src}
	f.checksumOK.Store(true)
	f.helloV2 = []byte(strings.Replace(string(src), `return "1.0.0"`, `return "1.1.0"`, 1))
	f.badName = []byte(strings.Replace(string(src), `return "hello"`, `return "other"`, 1))
	if string(f.helloV2) == string(src) || string(f.badName) == string(src) {
		t.Fatal("fixture replacements did not apply; check hello.go.example")
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.index())
		case "/hello.go":
			w.Write(f.helloV1)
		case "/hello-v2.go":
			w.Write(f.helloV2)
		case "/badname.go":
			w.Write(f.badName)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) entry(name, version, file, minVersion string, stars int) map[string]any {
	var src []byte
	switch file {
	case "/hello.go":
		src = f.helloV1
	case "/hello-v2.go":
		src = f.helloV2
	case "/badname.go":
		src = f.badName
	}
	sha := sum(src)
	if name == "hello" && !f.checksumOK.Load() {
		sha = strings.Repeat("0", 64)
	}
	return map[string]any{
		"name": name, "display_name": strings.Title(name), "description": "d", "version": version,
		"author": "a", "license": "MIT", "source_url": "https://github.com/x/" + name,
		"download_url": f.srv.URL + file, "sha256": sha, "min_goblog_version": minVersion,
		"install_type": "dynamic", "released_at": "2026-09-15T00:00:00Z",
		"detail_url": f.srv.URL + "/plugins/" + name + ".json", "stars": stars,
	}
}

// index is the default directory: hello 1.0.0 plus a compiled-in entry and a
// too-new one. Tests mutate f.entries to change it.
func (f *fixture) index() []map[string]any {
	if f.entries != nil {
		return f.entries
	}
	compiled := f.entry("scholar", "1.0.0", "/hello.go", "0.1.0", 50)
	compiled["install_type"] = "compiled-in"
	return []map[string]any{
		f.entry("hello", "1.0.0", "/hello.go", "0.2.6", 3),
		compiled,
		f.entry("future", "1.0.0", "/badname.go", "9.0.0", 1),
	}
}

func newInstaller(t *testing.T, f *fixture) *Installer {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	return &Installer{
		Dir:       t.TempDir(),
		Registry:  reg,
		Directory: directory.NewFetcher(f.srv.Client()),
		Version:   "v0.2.7",
		Client:    f.srv.Client(),
		Enabled:   true,
		IndexURL:  func() string { return f.srv.URL + "/index.json" },
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	inst.Registry.Register(&compiledPlugin{})

	st := inst.Status()
	if !st.DynamicEnabled || st.DirectoryURL != f.srv.URL+"/index.json" || st.IndexError != "" || st.IndexFetchedAt == "" {
		t.Errorf("status = %+v", st)
	}
	if len(st.Installed) != 1 || st.Installed[0].Name != "scholar" || st.Installed[0].Dynamic || st.Installed[0].UpdateAvailable {
		t.Errorf("installed = %+v", st.Installed)
	}
	// scholar is installed (compiled-in) so it is not "available"; hello and future are.
	if len(st.Available) != 2 || st.Available[0].Name != "hello" || st.Available[1].Name != "future" {
		t.Fatalf("available = %+v", st.Available)
	}
	if !st.Available[0].Compatible || st.Available[1].Compatible || !strings.Contains(st.Available[1].Reason, "9.0.0") {
		t.Errorf("compatibility: %+v", st.Available)
	}
	if st.Available[0].Stars != 3 {
		t.Errorf("stars should come through, got %d", st.Available[0].Stars)
	}
}

func TestStatus_DirectoryUnavailable(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	inst.IndexURL = func() string { return "http://127.0.0.1:1/index.json" }
	st := inst.Status()
	if st.IndexError == "" || len(st.Available) != 0 {
		t.Errorf("expected an index error and no available plugins, got %+v", st)
	}
	if _, err := inst.Install(context.Background(), "hello"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Errorf("install without an index should be ErrDirectoryUnavailable, got %v", err)
	}
}

func TestInstall_HappyPath(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)

	res, err := inst.Install(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "hello" || res.Version != "1.0.0" {
		t.Errorf("result = %+v", res)
	}
	path := filepath.Join(inst.Dir, "hello.go")
	if b, err := os.ReadFile(path); err != nil || string(b) != string(f.helloV1) {
		t.Fatalf("plugin file not written verbatim: %v", err)
	}
	dyn := inst.Registry.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "hello" || dyn[0].Path != path {
		t.Errorf("registry dynamic = %+v", dyn)
	}
	// Settings seeded (InitPlugin ran).
	seeded := false
	for _, g := range inst.Registry.GetAllSettings() {
		if g.PluginName == "hello" && g.CurrentValues["message"] != "" {
			seeded = true
		}
	}
	if !seeded {
		t.Error("hello's settings should be seeded after install")
	}
	st := inst.Status()
	if len(st.Installed) != 1 || !st.Installed[0].Dynamic || st.Installed[0].UpdateAvailable {
		t.Errorf("installed after install = %+v", st.Installed)
	}
	if _, err := inst.Install(context.Background(), "hello"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("second install should be ErrAlreadyInstalled, got %v", err)
	}
}

func TestInstall_Refusals(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	ctx := context.Background()

	if _, err := inst.Install(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := inst.Install(ctx, "scholar"); !errors.Is(err, ErrNotDynamic) {
		t.Errorf("compiled-in: %v", err)
	}
	if _, err := inst.Install(ctx, "future"); !errors.Is(err, ErrIncompatible) {
		t.Errorf("too new: %v", err)
	}

	f.checksumOK.Store(false)
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(ctx, "hello"); !errors.Is(err, ErrChecksum) {
		t.Errorf("bad checksum: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go")); err == nil {
		t.Error("nothing may be written on a checksum mismatch")
	}
	f.checksumOK.Store(true)

	// Name() disagrees with the index entry.
	f.entries = []map[string]any{f.entry("hello", "1.0.0", "/badname.go", "0.2.6", 0)}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(ctx, "hello"); !errors.Is(err, ErrLoad) {
		t.Errorf("name mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go")); err == nil {
		t.Error("nothing may be written when the plugin's identity does not match")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing may be registered on a failed install")
	}

	inst.Enabled = false
	if _, err := inst.Install(ctx, "hello"); !errors.Is(err, ErrDynamicDisabled) {
		t.Errorf("disabled: %v", err)
	}
	if st := inst.Status(); st.DynamicEnabled || len(st.Available) == 0 {
		t.Error("Status should still list the directory when dynamic plugins are disabled")
	}
}

func TestUpdateAndRollback(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	ctx := context.Background()
	if _, err := inst.Install(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	inst.Registry.UpdateSetting("hello", "message", "keep me")

	// Index now offers 1.1.0.
	f.entries = []map[string]any{f.entry("hello", "1.1.0", "/hello-v2.go", "0.2.6", 0)}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if st := inst.Status(); !st.Installed[0].UpdateAvailable || st.Installed[0].LatestVersion != "1.1.0" {
		t.Errorf("update should be available: %+v", st.Installed)
	}
	res, err := inst.Update(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.1.0" || inst.Registry.Dynamic()[0].Version != "1.1.0" {
		t.Errorf("update result = %+v, registry = %+v", res, inst.Registry.Dynamic())
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go.prev")); err == nil {
		t.Error(".prev file should be removed after a successful update")
	}
	kept := false
	for _, g := range inst.Registry.GetAllSettings() {
		if g.PluginName == "hello" && g.CurrentValues["message"] == "keep me" {
			kept = true
		}
	}
	if !kept {
		t.Error("settings must survive an update")
	}
	if _, err := inst.Update(ctx, "hello"); err == nil {
		t.Error("updating when already at the index version should fail")
	}

	// Rollback: the index advertises 1.2.0 but serves a file whose Version() is 1.1.0.
	bad := f.entry("hello", "1.2.0", "/hello-v2.go", "0.2.6", 0)
	f.entries = []map[string]any{bad}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrLoad) {
		t.Errorf("version mismatch should be ErrLoad, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(inst.Dir, "hello.go")); string(b) != string(f.helloV2) {
		t.Error("the previous file must be restored after a failed update")
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 || dyn[0].Version != "1.1.0" {
		t.Errorf("the previous plugin must be registered again after a failed update, got %+v", dyn)
	}

	if _, err := inst.Update(ctx, "scholar"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("updating a non-dynamic plugin: %v", err)
	}
}

func TestUninstall(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	if _, err := inst.Install(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := inst.Uninstall("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go")); err == nil {
		t.Error("file should be deleted")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("plugin should be unregistered")
	}
	for _, g := range inst.Registry.GetAllSettings() {
		if g.PluginName == "hello" {
			t.Error("settings should be deleted on uninstall")
		}
	}
	if err := inst.Uninstall("hello"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("second uninstall: %v", err)
	}
	inst.Registry.Register(&compiledPlugin{})
	if err := inst.Uninstall("scholar"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("uninstalling compiled-in: %v", err)
	}
}

// compiledPlugin stands in for a compiled-in plugin named like an index entry.
type compiledPlugin struct{ plugin.BasePlugin }

func (compiledPlugin) Name() string        { return "scholar" }
func (compiledPlugin) DisplayName() string { return "Scholar" }
func (compiledPlugin) Version() string     { return "1.0.0" }
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./plugin/installer/ -v`
Expected: FAIL to compile — `undefined: Installer`, `compatible`, …

- [ ] **Step 3: Implement version helpers**

`plugin/installer/version.go`:

```go
package installer

import (
	"strconv"
	"strings"
)

// parseVersion reads "1.2.3" or "v1.2.3" into three ints.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func cmp(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// compatible reports whether a goblog at version running satisfies a
// plugin's min_goblog_version. Dev builds ("development", "latest", or
// anything unparseable) and an unparseable minimum are treated as compatible.
func compatible(running, min string) bool {
	r, ok := parseVersion(running)
	if !ok {
		return true
	}
	m, ok := parseVersion(min)
	if !ok {
		return true
	}
	return cmp(r, m) >= 0
}

// newer reports whether candidate is a strictly newer version than current.
func newer(candidate, current string) bool {
	c, ok1 := parseVersion(candidate)
	u, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	return cmp(c, u) > 0
}
```

- [ ] **Step 4: Implement the installer**

`plugin/installer/installer.go`:

```go
// Package installer installs, updates and removes dynamic plugins from the
// plugin directory at runtime, for the admin UI.
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goblog/plugin"
	"goblog/plugins/directory"
)

// DefaultIndexURL is the public plugin directory index.
const DefaultIndexURL = "https://www.goblog.live/plugins/index.json"

// maxPluginBytes caps a plugin download.
const maxPluginBytes = 1 << 20

// Typed errors; the admin API maps them to 4xx responses with their text.
var (
	ErrDynamicDisabled      = errors.New("dynamic plugins are disabled: start goblog with ENABLE_DYNAMIC_PLUGINS=true and a writable plugins/dynamic/ directory")
	ErrIncompatible         = errors.New("this plugin requires a newer goblog")
	ErrChecksum             = errors.New("the downloaded file does not match the checksum in the directory index; the index may be stale, refresh and try again")
	ErrNotDynamic           = errors.New("this plugin ships compiled into goblog and cannot be installed from the directory")
	ErrAlreadyInstalled     = errors.New("a plugin with this name is already installed")
	ErrNotInstalled         = errors.New("this plugin is not installed as a dynamic plugin")
	ErrNotFound             = errors.New("this plugin is not in the directory")
	ErrLoad                 = errors.New("the plugin failed to load")
	ErrDirectoryUnavailable = errors.New("the plugin directory index is unavailable")
)

// Installer wires the directory index, the plugin registry and the
// plugins/dynamic directory together.
type Installer struct {
	Dir       string             // plugins/dynamic
	Registry  *plugin.Registry
	Directory *directory.Fetcher // own instance; not the directory plugin's
	Version   string             // running goblog version, e.g. "v0.2.7" or "development"
	Client    *http.Client       // downloads; nil → 30s timeout default
	Enabled   bool               // ENABLE_DYNAMIC_PLUGINS
	IndexURL  func() string      // current plugin_directory_url setting
}

// Installed is a registered plugin as shown on the Installed tab.
type Installed struct {
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version"`
	Enabled         bool   `json:"enabled"`
	Dynamic         bool   `json:"dynamic"`
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version,omitempty"`
	Path            string `json:"path,omitempty"`
}

// Available is a directory entry that is not installed.
type Available struct {
	directory.Entry
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"` // why Install is disabled
}

// Status is everything the admin page needs in one call.
type Status struct {
	Installed      []Installed `json:"installed"`
	Available      []Available `json:"available"`
	DirectoryURL   string      `json:"directory_url"`
	DynamicEnabled bool        `json:"dynamic_enabled"`
	IndexFetchedAt string      `json:"index_fetched_at,omitempty"`
	IndexError     string      `json:"index_error,omitempty"`
}

// Result reports a successful install or update.
type Result struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Message string `json:"message"`
}

func (i *Installer) client() *http.Client {
	if i.Client != nil {
		return i.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Refresh re-fetches the index now.
func (i *Installer) Refresh() error {
	if err := i.Directory.Refresh(i.IndexURL()); err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}
	return nil
}

// Status lists installed plugins (with update info for dynamic ones) and the
// directory entries that are not installed, sorted by stars then name.
func (i *Installer) Status() Status {
	indexURL := i.IndexURL()
	i.Directory.Ensure(indexURL)
	_, entries, ok := i.Directory.Index()
	st := Status{DirectoryURL: indexURL, DynamicEnabled: i.Enabled, Installed: []Installed{}, Available: []Available{}}
	if !ok {
		st.IndexError = "could not fetch the plugin directory index"
	} else {
		st.IndexFetchedAt = i.Directory.FetchedAt().UTC().Format(time.RFC3339)
	}

	byName := make(map[string]directory.Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	dynamic := map[string]plugin.DynamicInfo{}
	for _, d := range i.Registry.Dynamic() {
		dynamic[d.Name] = d
	}
	installed := map[string]bool{}
	for _, p := range i.Registry.Plugins() {
		installed[p.Name()] = true
		row := Installed{Name: p.Name(), DisplayName: p.DisplayName(), Version: p.Version(), Enabled: i.Registry.IsPluginEnabled(p.Name())}
		if d, ok := dynamic[p.Name()]; ok {
			row.Dynamic = true
			row.Path = d.Path
			if e, ok := byName[p.Name()]; ok {
				row.LatestVersion = e.Version
				row.UpdateAvailable = newer(e.Version, p.Version())
			}
		}
		st.Installed = append(st.Installed, row)
	}
	for _, e := range entries {
		if installed[e.Name] {
			continue
		}
		a := Available{Entry: e, Compatible: true}
		switch {
		case e.InstallType != "dynamic":
			a.Compatible, a.Reason = false, "ships compiled into goblog"
		case !compatible(i.Version, e.MinGoblogVersion):
			a.Compatible, a.Reason = false, "requires goblog "+e.MinGoblogVersion+" or newer"
		}
		st.Available = append(st.Available, a)
	}
	sort.SliceStable(st.Available, func(x, y int) bool {
		if st.Available[x].Stars != st.Available[y].Stars {
			return st.Available[x].Stars > st.Available[y].Stars
		}
		return st.Available[x].Name < st.Available[y].Name
	})
	return st
}

// lookup finds an index entry and applies the checks that do not need the
// file: dynamic, compatible.
func (i *Installer) lookup(name string) (directory.Entry, error) {
	i.Directory.Ensure(i.IndexURL())
	if _, _, ok := i.Directory.Index(); !ok {
		return directory.Entry{}, ErrDirectoryUnavailable
	}
	e, ok := i.Directory.Entry(name)
	if !ok {
		return directory.Entry{}, ErrNotFound
	}
	if e.InstallType != "dynamic" {
		return directory.Entry{}, ErrNotDynamic
	}
	if !compatible(i.Version, e.MinGoblogVersion) {
		return directory.Entry{}, fmt.Errorf("%w (%s; this is %s)", ErrIncompatible, e.MinGoblogVersion, i.Version)
	}
	return e, nil
}

func (i *Installer) isRegistered(name string) bool {
	for _, p := range i.Registry.Plugins() {
		if p.Name() == name {
			return true
		}
	}
	return false
}

func (i *Installer) dynamic(name string) (plugin.DynamicInfo, bool) {
	for _, d := range i.Registry.Dynamic() {
		if d.Name == name {
			return d, true
		}
	}
	return plugin.DynamicInfo{}, false
}

// Install downloads, verifies, writes, loads and registers a directory plugin.
func (i *Installer) Install(ctx context.Context, name string) (Result, error) {
	if !i.Enabled {
		return Result{}, ErrDynamicDisabled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if i.isRegistered(name) {
		return Result{}, ErrAlreadyInstalled
	}
	src, p, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(i.Dir, e.Name+".go")
	if err := writeAtomic(path, src); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := i.register(p, path); err != nil {
		os.Remove(path)
		return Result{}, err
	}
	log.Printf("Installed plugin %s v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Installed %s v%s", e.DisplayName, e.Version)}, nil
}

// Update replaces an installed dynamic plugin with the index version. If the
// new file fails to load, the previous file and plugin are restored.
func (i *Installer) Update(ctx context.Context, name string) (Result, error) {
	if !i.Enabled {
		return Result{}, ErrDynamicDisabled
	}
	d, ok := i.dynamic(name)
	if !ok {
		return Result{}, ErrNotInstalled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if !newer(e.Version, d.Version) {
		return Result{}, fmt.Errorf("%s is already at v%s; the directory has v%s", name, d.Version, e.Version)
	}
	src, p, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}

	var old plugin.Plugin
	for _, rp := range i.Registry.Plugins() {
		if rp.Name() == name {
			old = rp
		}
	}
	prev := d.Path + ".prev"
	if err := os.Rename(d.Path, prev); err != nil {
		return Result{}, fmt.Errorf("back up %s: %w", d.Path, err)
	}
	rollback := func(cause error) (Result, error) {
		i.Registry.Unregister(name) // no-op if the new plugin never registered
		if err := os.Rename(prev, d.Path); err != nil {
			log.Printf("Update %s: restoring %s failed: %v", name, d.Path, err)
		}
		if old != nil {
			if err := i.Registry.RegisterDynamic(old, d.Path); err == nil {
				if err := i.Registry.InitPlugin(name); err != nil {
					log.Printf("Update %s: re-initialising previous version failed: %v", name, err)
				}
			}
		}
		return Result{}, cause
	}
	if err := i.Registry.Unregister(name); err != nil {
		return rollback(err)
	}
	if err := writeAtomic(d.Path, src); err != nil {
		return rollback(fmt.Errorf("write %s: %w", d.Path, err))
	}
	if err := i.register(p, d.Path); err != nil {
		return rollback(err)
	}
	os.Remove(prev)
	log.Printf("Updated plugin %s to v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Updated %s to v%s", e.DisplayName, e.Version)}, nil
}

// Uninstall unregisters a dynamic plugin, deletes its file and its settings.
func (i *Installer) Uninstall(name string) error {
	if !i.Enabled {
		return ErrDynamicDisabled
	}
	d, ok := i.dynamic(name)
	if !ok {
		return ErrNotInstalled
	}
	if err := i.Registry.Unregister(name); err != nil {
		return err
	}
	if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", d.Path, err)
	}
	i.Registry.DeleteSettings(name)
	log.Printf("Uninstalled plugin %s", name)
	return nil
}

// fetchAndCheck downloads the entry's file and proves it is what the index
// says: checksum, loads in the interpreter, and reports the same name and
// version. Nothing is written or registered here.
func (i *Installer) fetchAndCheck(ctx context.Context, e directory.Entry) ([]byte, plugin.Plugin, error) {
	src, err := i.download(ctx, e.DownloadURL)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(src)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), e.SHA256) {
		return nil, nil, ErrChecksum
	}
	p, err := plugin.LoadDynamicPluginBytes(src)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if p.Name() != e.Name {
		return nil, nil, fmt.Errorf("%w: it reports Name() %q but the directory lists it as %q", ErrLoad, p.Name(), e.Name)
	}
	if p.Version() != e.Version {
		return nil, nil, fmt.Errorf("%w: it reports Version() %q but the directory lists v%s", ErrLoad, p.Version(), e.Version)
	}
	return src, p, nil
}

func (i *Installer) register(p plugin.Plugin, path string) error {
	if err := i.Registry.RegisterDynamic(p, path); err != nil {
		return fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if err := i.Registry.InitPlugin(p.Name()); err != nil {
		i.Registry.Unregister(p.Name())
		return fmt.Errorf("%w: %v", ErrLoad, err)
	}
	return nil
}

// download fetches a plugin file over HTTPS (plain HTTP only to loopback,
// for tests) with a size cap.
func (i *Installer) download(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: bad download_url: %v", ErrLoad, err)
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")) {
		return nil, fmt.Errorf("%w: download_url must be https", ErrLoad)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "goblog-installer/"+i.Version)
	resp, err := i.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	src, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if len(src) > maxPluginBytes {
		return nil, fmt.Errorf("%w: file is larger than %d bytes", ErrLoad, maxPluginBytes)
	}
	return src, nil
}

// writeAtomic writes via a temp file in the same directory and renames it
// into place, so a crash never leaves a half-written plugin.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plugin-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
```

The `directory.Entry` type needs a `Stars int \`json:"stars"\`` field for `TestStatus` — add it in `plugins/directory/index.go` now (after `DetailURL`):

```go
	Stars            int    `json:"stars"` // GitHub stargazers, added by the registry build
```

- [ ] **Step 5: Run tests, commit**

Run: `go test -race ./plugin/installer/ ./plugins/directory/ -v -count=1 && go vet ./plugin/installer/`
Expected: PASS for all (Yaegi loads take ~100 ms each; the package should finish in a few seconds).

```bash
git branch --show-current
git add plugin/installer/ plugins/directory/index.go
git commit -m "Add the plugin installer: download, verify, load and hot-register directory plugins (#553)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Admin API, setting, wiring

**Files:**
- Create: `admin/plugins.go`
- Modify: `admin/admin.go:28-34` (struct field), `blog/blog.go` (add `SettingValue`), `tools/migrate.go:294-308` (seed), `goblog.go` (installer + routes)
- Test: `admin/plugins_test.go`, `blog/blog_test.go` (append)

**Interfaces:**
- Consumes: `installer.Installer` and its errors/types (Task 2).
- Produces: `Admin.Installer *installer.Installer`; handlers `PluginStatus`, `PluginDirectory`, `InstallPlugin`, `UpdatePlugin`, `UninstallPlugin`, `RefreshPluginDirectory`, `AdminPlugins`; `blog.(*Blog).SettingValue(key, def string) string`.

- [ ] **Step 1: Write the failing tests**

Append to `blog/blog_test.go`:

```go
func TestSettingValue(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Setting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	if got := b.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("missing setting should return the default, got %q", got)
	}
	db.Create(&blog.Setting{Key: "plugin_directory_url", Type: "text", Value: "https://x.test/i.json"})
	if got := b.SettingValue("plugin_directory_url", "default"); got != "https://x.test/i.json" {
		t.Errorf("got %q", got)
	}
	db.Model(&blog.Setting{}).Where("key = ?", "plugin_directory_url").Update("value", "")
	if got := b.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("empty setting should return the default, got %q", got)
	}
	nodb := blog.New(nil, a, "test")
	if got := nodb.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("no db should return the default, got %q", got)
	}
}
```

`admin/plugins_test.go` (package `admin_test`; the file's `Auth` mock and imports for gin/sessions/sqlite/gorm already exist in `admin_test.go`):

```go
package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"goblog/plugin/installer"
	"goblog/plugins/directory"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// pluginsHarness wires a router with the plugin API against a fixture index.
type pluginsHarness struct {
	router *gin.Engine
	auth   *Auth
	inst   *installer.Installer
	srv    *httptest.Server
	ad     admin.Admin
}

func newPluginsHarness(t *testing.T) *pluginsHarness {
	t.Helper()
	src, err := os.ReadFile("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256hex(src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason","license":"MIT","source_url":"https://github.com/x/hello","download_url":"http://127.0.0.1/hello.go","sha256":"` + sha + `","min_goblog_version":"0.2.6","install_type":"dynamic","released_at":"2026-09-15T00:00:00Z","detail_url":"","stars":7},
			{"name":"zeta","display_name":"Zeta","description":"Other thing","version":"0.1.0","author":"Someone","license":"MIT","source_url":"https://github.com/x/zeta","download_url":"https://example.test/z.go","sha256":"00","min_goblog_version":"0.1.0","install_type":"dynamic","released_at":"2026-01-01T00:00:00Z","detail_url":"","stars":1}]`))
		case "/hello.go":
			w.Write(src)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	reg := plugin.NewRegistry(db)
	reg.Init()
	inst := &installer.Installer{
		Dir: t.TempDir(), Registry: reg, Directory: directory.NewFetcher(srv.Client()),
		Version: "v0.2.7", Client: rewritingClient(srv), Enabled: true,
		IndexURL: func() string { return srv.URL + "/index.json" },
	}
	ad.Installer = inst

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	router.GET("/api/v1/plugins/status", ad.PluginStatus)
	router.GET("/api/v1/plugins/directory", ad.PluginDirectory)
	router.POST("/api/v1/plugins/install", ad.InstallPlugin)
	router.POST("/api/v1/plugins/update", ad.UpdatePlugin)
	router.DELETE("/api/v1/plugins/:name", ad.UninstallPlugin)
	router.POST("/api/v1/plugins/refresh", ad.RefreshPluginDirectory)
	return &pluginsHarness{router: router, auth: a, inst: inst, srv: srv, ad: ad}
}

func (h *pluginsHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func TestPluginAPI_NonAdmin(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/v1/plugins/status"}, {"GET", "/api/v1/plugins/directory"},
		{"POST", "/api/v1/plugins/install"}, {"POST", "/api/v1/plugins/update"},
		{"DELETE", "/api/v1/plugins/hello"}, {"POST", "/api/v1/plugins/refresh"},
	} {
		if w := h.do(c.m, c.p, `{"name":"hello"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", c.m, c.p, w.Code)
		}
	}
}

func TestPluginAPI_StatusDirectoryInstallUninstall(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	w := h.do("GET", "/api/v1/plugins/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var st installer.Status
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.DynamicEnabled || len(st.Available) != 2 || st.Available[0].Name != "hello" {
		t.Errorf("status = %+v", st)
	}

	// Directory search + sort.
	w = h.do("GET", "/api/v1/plugins/directory?q=other", "")
	var avail []installer.Available
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 1 || avail[0].Name != "zeta" {
		t.Errorf("search 'other' = %+v", avail)
	}
	w = h.do("GET", "/api/v1/plugins/directory?sort=name", "")
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 2 || avail[0].Name != "hello" || avail[1].Name != "zeta" {
		t.Errorf("sort=name = %+v", avail)
	}
	w = h.do("GET", "/api/v1/plugins/directory?sort=newest", "")
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 2 || avail[0].Name != "hello" {
		t.Errorf("sort=newest = %+v", avail)
	}

	// Install.
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Installed Hello v1.0.0") {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already installed") {
		t.Errorf("second install: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", w.Code)
	}
	w = h.do("POST", "/api/v1/plugins/install", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing name: %d", w.Code)
	}
	w = h.do("POST", "/api/v1/plugins/update", `{"name":"hello"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update at latest should be 400, got %d %s", w.Code, w.Body.String())
	}

	// Uninstall.
	w = h.do("DELETE", "/api/v1/plugins/hello", "")
	if w.Code != http.StatusOK {
		t.Errorf("uninstall: %d %s", w.Code, w.Body.String())
	}
	w = h.do("DELETE", "/api/v1/plugins/hello", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("second uninstall: %d", w.Code)
	}

	// Disabled: 4xx with the message, status still fine.
	h.inst.Enabled = false
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "ENABLE_DYNAMIC_PLUGINS") {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/refresh", "")
	if w.Code != http.StatusOK {
		t.Errorf("refresh: %d %s", w.Code, w.Body.String())
	}
}

func TestPluginAPI_NoInstaller(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(true)
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	router := gin.New()
	router.GET("/api/v1/plugins/status", ad.PluginStatus)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/plugins/status", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("without an installer the API should answer 503, got %d", w.Code)
	}
}

// rewritingClient sends requests for the port-less loopback host used in the
// fixture index (http://127.0.0.1/hello.go — plain HTTP is only accepted for
// loopback) to the fixture server's real port.
func rewritingClient(srv *httptest.Server) *http.Client {
	base := srv.Client()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "127.0.0.1" {
			u := *r.URL
			real, _ := http.NewRequest(r.Method, srv.URL+u.Path, nil)
			real.Header = r.Header
			return base.Transport.RoundTrip(real)
		}
		return base.Transport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
```

Add `"crypto/sha256"` and `"encoding/hex"` to that file's imports. The fixture's hello `download_url` is `http://127.0.0.1/hello.go` on purpose: the installer accepts plain HTTP only for loopback hosts, and `rewritingClient` routes that port-less host to the fixture server.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./blog/ -run TestSettingValue -v; go test ./admin/ -run TestPluginAPI -v`
Expected: FAIL to compile — `b.SettingValue undefined`, `ad.Installer undefined`, `ad.PluginStatus undefined`.

- [ ] **Step 3: Implement**

`blog/blog.go` — add after `GetSettings`:

```go
// SettingValue returns one site setting, or def when the database is not
// ready, the setting is missing, or it is empty.
func (b *Blog) SettingValue(key, def string) string {
	if b.db == nil || *b.db == nil {
		return def
	}
	var s Setting
	if err := (*b.db).Where("key = ?", key).First(&s).Error; err != nil || s.Value == "" {
		return def
	}
	return s.Value
}
```

`tools/migrate.go` `seedDefaultSettings` — add after the `comments_require_login` line:

```go
		{Key: "plugin_directory_url", Type: "text", Value: "https://www.goblog.live/plugins/index.json"},
```

`admin/admin.go` — add the field to `Admin`:

```go
	Installer     *installer.Installer  // plugin directory install/update; nil when not wired
```
and import `"goblog/plugin/installer"`.

`admin/plugins.go`:

```go
package admin

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"goblog/plugin/installer"

	"github.com/gin-gonic/gin"
)

// requireInstaller checks admin auth and that the installer is wired. It
// writes the response and returns nil when the caller should stop.
func (a *Admin) requireInstaller(c *gin.Context) *installer.Installer {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return nil
	}
	if a.Installer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "plugin installer is not available"})
		return nil
	}
	return a.Installer
}

// installerStatus maps installer errors to HTTP status codes.
func installerStatus(err error) int {
	switch {
	case errors.Is(err, installer.ErrNotFound), errors.Is(err, installer.ErrNotInstalled):
		return http.StatusNotFound
	case errors.Is(err, installer.ErrAlreadyInstalled):
		return http.StatusConflict
	case errors.Is(err, installer.ErrDynamicDisabled):
		return http.StatusPreconditionFailed
	case errors.Is(err, installer.ErrIncompatible), errors.Is(err, installer.ErrNotDynamic),
		errors.Is(err, installer.ErrChecksum), errors.Is(err, installer.ErrLoad):
		return http.StatusUnprocessableEntity
	case errors.Is(err, installer.ErrDirectoryUnavailable):
		return http.StatusBadGateway
	}
	return 0
}

func writeInstallerError(c *gin.Context, err error) {
	if code := installerStatus(err); code != 0 {
		c.JSON(code, gin.H{"message": err.Error()})
		return
	}
	log.Printf("plugin installer: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "plugin operation failed; see the server log"})
}

// PluginStatus returns installed plugins and the directory entries not yet installed.
func (a *Admin) PluginStatus(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	c.JSON(http.StatusOK, inst.Status())
}

// PluginDirectory returns available plugins filtered by ?q= and ordered by ?sort= (stars|name|newest).
func (a *Admin) PluginDirectory(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	avail := inst.Status().Available // already sorted by stars, name
	if q := strings.ToLower(strings.TrimSpace(c.Query("q"))); q != "" {
		var filtered []installer.Available
		for _, p := range avail {
			hay := strings.ToLower(p.Name + " " + p.DisplayName + " " + p.Description + " " + p.Author)
			if strings.Contains(hay, q) {
				filtered = append(filtered, p)
			}
		}
		avail = filtered
	}
	switch c.Query("sort") {
	case "name":
		sort.SliceStable(avail, func(i, j int) bool { return avail[i].Name < avail[j].Name })
	case "newest":
		sort.SliceStable(avail, func(i, j int) bool { return avail[i].ReleasedAt > avail[j].ReleasedAt })
	}
	if avail == nil {
		avail = []installer.Available{}
	}
	c.JSON(http.StatusOK, avail)
}

type pluginRequest struct {
	Name string `json:"name"`
}

func bindPluginName(c *gin.Context) (string, bool) {
	var req pluginRequest
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "plugin name is required"})
		return "", false
	}
	return strings.TrimSpace(req.Name), true
}

// InstallPlugin installs a directory plugin: POST {name}.
func (a *Admin) InstallPlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	name, ok := bindPluginName(c)
	if !ok {
		return
	}
	res, err := inst.Install(c.Request.Context(), name)
	if err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// UpdatePlugin updates an installed dynamic plugin to the directory version: POST {name}.
func (a *Admin) UpdatePlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	name, ok := bindPluginName(c)
	if !ok {
		return
	}
	res, err := inst.Update(c.Request.Context(), name)
	if err != nil {
		if installerStatus(err) == 0 {
			// "already at this version" and similar are user-facing, not server faults
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// UninstallPlugin removes a dynamic plugin: DELETE /api/v1/plugins/:name.
func (a *Admin) UninstallPlugin(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	if err := inst.Uninstall(c.Param("name")); err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Uninstalled " + c.Param("name")})
}

// RefreshPluginDirectory re-fetches the directory index now.
func (a *Admin) RefreshPluginDirectory(c *gin.Context) {
	inst := a.requireInstaller(c)
	if inst == nil {
		return
	}
	if err := inst.Refresh(); err != nil {
		writeInstallerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Directory refreshed"})
}

// AdminPlugins renders the Plugins admin page; the data is loaded by the page over the API.
func (a *Admin) AdminPlugins(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin_plugins.html", gin.H{
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}
```

`goblog.go` — after the registry is built and `dir` registered (before `LoadDynamicPlugins`), create the installer and hand it to admin; add routes:

```go
	dynamicEnabled := os.Getenv("ENABLE_DYNAMIC_PLUGINS") == "true"
	if dynamicEnabled {
		gplugin.LoadDynamicPlugins(registry, "plugins/dynamic")
	}
	pluginInstaller := &installer.Installer{
		Dir:       "plugins/dynamic",
		Registry:  registry,
		Directory: directory.NewFetcher(nil),
		Version:   Version,
		Enabled:   dynamicEnabled,
		IndexURL:  func() string { return _blog.SettingValue("plugin_directory_url", installer.DefaultIndexURL) },
	}
	pluginInstaller.Directory.SetUserAgent("goblog-installer/" + Version)
	_admin.Installer = pluginInstaller
```
(`_blog` is the `blog.Blog` value declared earlier in `main`; `_blog.SettingValue` needs a pointer receiver — `_blog` is addressable so `_blog.SettingValue(...)` works.) Import `"goblog/plugin/installer"`.

Routes, next to `router.PATCH("/api/v1/plugin-settings", ...)`:

```go
	router.GET("/api/v1/plugins/status", goblog._admin.PluginStatus)
	router.GET("/api/v1/plugins/directory", goblog._admin.PluginDirectory)
	router.POST("/api/v1/plugins/install", goblog._admin.InstallPlugin)
	router.POST("/api/v1/plugins/update", goblog._admin.UpdatePlugin)
	router.DELETE("/api/v1/plugins/:name", goblog._admin.UninstallPlugin)
	router.POST("/api/v1/plugins/refresh", goblog._admin.RefreshPluginDirectory)
```
and in `addRoutesInner` next to `/admin/users`:
```go
	g.router.GET("/admin/plugins", g._admin.AdminPlugins)
```

- [ ] **Step 4: Run tests, commit**

Run: `go build ./... && go vet ./admin/ ./blog/ ./tools/ && go test -race ./admin/ ./blog/ -count=1`
Expected: PASS. (`admin_plugins.html` does not exist yet; `AdminPlugins` is not exercised by these tests — Task 4 adds it.)

```bash
git branch --show-current
git add admin/ blog/blog.go blog/blog_test.go tools/migrate.go goblog.go
git commit -m "Add the admin plugin API and plugin_directory_url setting (#553)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `/admin/plugins` page

**Files:**
- Create: `themes/default/templates/admin_plugins.html`
- Modify: `themes/default/templates/admin_nav.html:9-10` (add link)
- Test: `admin/plugins_test.go` (append a render test)

**Interfaces:**
- Consumes: the API from Task 3 (paths and JSON shapes: `Status`, `Available`, `Result`, `{message}`), existing `PATCH /api/v1/plugin-settings` (`[{key:"<plugin>.enabled", value:"true"|"false"}]`).

- [ ] **Step 1: Write the failing render test**

Append to `admin/plugins_test.go`:

```go
func TestAdminPluginsPage(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	h.router.SetHTMLTemplate(tmpl)
	h.router.GET("/admin/plugins", func(c *gin.Context) { adminFromHarness(h).AdminPlugins(c) })

	w := h.do("GET", "/admin/plugins", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`id="tab-installed"`, `id="tab-browse"`, `/api/v1/plugins/status`, `href="/admin/plugins"`, "ENABLE_DYNAMIC_PLUGINS"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}
```

`pluginsHarness.ad` is the `admin.Admin` built in `newPluginsHarness`; add `func adminFromHarness(h *pluginsHarness) *admin.Admin { return &h.ad }` and `"html/template"` to the imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./admin/ -run TestAdminPluginsPage -v`
Expected: FAIL — `html/template: "admin_plugins.html" is undefined` (500) or missing markers.

- [ ] **Step 3: Write the template and nav link**

`themes/default/templates/admin_nav.html` — insert between the Post Types and Settings links:

```html
            <a class="nav-link" href="/admin/plugins">Plugins</a>
```

`themes/default/templates/admin_plugins.html`:

```html
{{ template "header.html" .}}
<div class="container">
    {{ template "admin_nav.html" . }}
    <h1>Plugins</h1>
    <p class="text-muted">Browse the <a href="https://www.goblog.live/plugins" target="_blank" rel="noopener">plugin directory</a> and install plugins with one click. Directory plugins are dynamic: they can add settings and head/footer HTML. Plugins that add pages or background jobs ship compiled into goblog.</p>

    <div id="plugins-error" class="alert alert-danger" role="alert" hidden></div>
    <div id="plugins-disabled" class="alert alert-warning" role="alert" hidden>
        Installing plugins needs <code>ENABLE_DYNAMIC_PLUGINS=true</code> and a writable, persisted <code>plugins/dynamic/</code> directory (bind-mount it with Docker). See the README under <em>Dynamic plugins</em>. You can still browse the directory.
    </div>
    <div id="plugins-index-error" class="alert alert-warning" role="alert" hidden>
        <span id="plugins-index-error-text"></span> — directory URL is set under <a href="/admin/settings">Settings → plugin_directory_url</a>.
        <button type="button" class="btn btn-sm btn-outline-secondary ms-2" onclick="refreshDirectory()">Refresh</button>
    </div>
    <div id="plugins-trust" class="alert alert-info alert-dismissible" role="alert">
        Plugins run as Go code inside goblog with the same access it has. Install only plugins from sources you trust.
        <button type="button" class="btn-close" data-bs-dismiss="alert" aria-label="Close"></button>
    </div>

    <ul class="nav nav-tabs mb-3" role="tablist">
        <li class="nav-item"><button class="nav-link active" id="tab-installed" data-bs-toggle="tab" data-bs-target="#pane-installed" type="button" role="tab">Installed</button></li>
        <li class="nav-item"><button class="nav-link" id="tab-browse" data-bs-toggle="tab" data-bs-target="#pane-browse" type="button" role="tab">Browse</button></li>
    </ul>
    <div class="tab-content">
        <div class="tab-pane fade show active" id="pane-installed" role="tabpanel">
            <table class="table align-middle">
                <thead><tr><th>Plugin</th><th>Version</th><th>Type</th><th>Enabled</th><th></th></tr></thead>
                <tbody id="installed-rows"><tr><td colspan="5" class="text-muted">Loading…</td></tr></tbody>
            </table>
        </div>
        <div class="tab-pane fade" id="pane-browse" role="tabpanel">
            <div class="row g-2 mb-3">
                <div class="col-md-8"><input type="search" id="browse-q" class="form-control" placeholder="Search plugins by name, description or author" oninput="scheduleBrowse()"></div>
                <div class="col-md-3">
                    <select id="browse-sort" class="form-select" onchange="loadBrowse()">
                        <option value="stars">Most stars</option>
                        <option value="name">Name</option>
                        <option value="newest">Newest release</option>
                    </select>
                </div>
                <div class="col-md-1 d-grid"><button type="button" class="btn btn-outline-secondary" onclick="refreshDirectory()" title="Refresh the directory index">↻</button></div>
            </div>
            <div id="browse-cards" class="row g-3"><div class="col-12 text-muted">Loading…</div></div>
        </div>
    </div>
</div>

<script>
(function () {
  var state = { status: null, dynamicEnabled: false };

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (ch) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch];
    });
  }
  function showError(msg) {
    var el = document.getElementById("plugins-error");
    el.textContent = msg; el.hidden = !msg;
  }
  function api(method, path, body) {
    return fetch(path, {
      method: method,
      headers: { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (resp) {
      return resp.json().catch(function () { return {}; }).then(function (data) {
        if (!resp.ok) { throw new Error(data.message || (typeof data === "string" ? data : "Request failed (" + resp.status + ")")); }
        return data;
      });
    });
  }

  function renderInstalled(st) {
    var rows = st.installed.map(function (p) {
      var actions = "";
      if (p.dynamic && p.update_available) {
        actions += '<button type="button" class="btn btn-sm btn-primary me-1" onclick="pluginAction(\'update\', \'' + esc(p.name) + '\', this)">Update to v' + esc(p.latest_version) + '</button>';
      }
      if (p.dynamic) {
        actions += '<button type="button" class="btn btn-sm btn-outline-danger me-1" onclick="pluginAction(\'uninstall\', \'' + esc(p.name) + '\', this)">Uninstall</button>';
      }
      actions += '<a class="btn btn-sm btn-link" href="/admin/settings#plugin-body-' + esc(p.name) + '">Settings</a>';
      return '<tr id="installed-' + esc(p.name) + '">' +
        '<td><strong>' + esc(p.display_name) + '</strong><br><small class="text-muted">' + esc(p.name) + '</small></td>' +
        '<td>v' + esc(p.version) + '</td>' +
        '<td>' + (p.dynamic ? '<span class="badge text-bg-secondary">dynamic</span>' : '<span class="badge text-bg-light">built-in</span>') + '</td>' +
        '<td><div class="form-check form-switch"><input class="form-check-input" type="checkbox" ' + (p.enabled ? "checked" : "") + ' onchange="toggleEnabled(\'' + esc(p.name) + '\', this.checked, this)"></div></td>' +
        '<td class="text-end">' + actions + '<div class="small text-danger row-error"></div></td></tr>';
    });
    document.getElementById("installed-rows").innerHTML = rows.join("") || '<tr><td colspan="5" class="text-muted">No plugins registered.</td></tr>';
  }

  function renderBrowse(list) {
    var cards = list.map(function (p) {
      var disabled = !state.dynamicEnabled || !p.compatible;
      var reason = !state.dynamicEnabled ? "dynamic plugins are disabled" : (p.reason || "");
      return '<div class="col-md-6"><div class="card h-100"><div class="card-body">' +
        '<h5 class="card-title mb-1">' + esc(p.display_name) + ' <small class="text-muted">v' + esc(p.version) + '</small></h5>' +
        '<p class="mb-1 small text-muted">by ' + esc(p.author) + ' · ★ ' + esc(p.stars) + ' · ' + esc(p.license) + ' · requires goblog ≥ ' + esc(p.min_goblog_version) + '</p>' +
        '<p class="card-text">' + esc(p.description) + '</p>' +
        '<a href="' + esc(p.source_url) + '" target="_blank" rel="noopener" class="me-2">Source</a>' +
        '<a href="https://www.goblog.live/plugins/' + esc(p.name) + '" target="_blank" rel="noopener">Directory page</a>' +
        '</div><div class="card-footer d-flex justify-content-between align-items-center">' +
        '<span class="small text-muted">' + esc(reason) + '</span>' +
        '<button type="button" class="btn btn-sm btn-primary" ' + (disabled ? "disabled" : "") + ' onclick="pluginAction(\'install\', \'' + esc(p.name) + '\', this)">Install</button>' +
        '</div><div class="small text-danger px-3 pb-2 row-error"></div></div></div>';
    });
    document.getElementById("browse-cards").innerHTML = cards.join("") || '<div class="col-12 text-muted">No plugins match.</div>';
  }

  function loadStatus() {
    return api("GET", "/api/v1/plugins/status").then(function (st) {
      state.status = st; state.dynamicEnabled = st.dynamic_enabled;
      document.getElementById("plugins-disabled").hidden = st.dynamic_enabled;
      var ie = document.getElementById("plugins-index-error");
      ie.hidden = !st.index_error;
      document.getElementById("plugins-index-error-text").textContent = st.index_error || "";
      renderInstalled(st);
      return loadBrowse();
    }).catch(function (e) { showError(e.message); });
  }

  var browseTimer = null;
  window.scheduleBrowse = function () { clearTimeout(browseTimer); browseTimer = setTimeout(loadBrowse, 200); };
  window.loadBrowse = function () {
    var q = encodeURIComponent(document.getElementById("browse-q").value);
    var sort = document.getElementById("browse-sort").value;
    return api("GET", "/api/v1/plugins/directory?q=" + q + "&sort=" + sort).then(renderBrowse).catch(function (e) { showError(e.message); });
  };
  window.refreshDirectory = function () {
    showError("");
    api("POST", "/api/v1/plugins/refresh").then(loadStatus).catch(function (e) { showError(e.message); });
  };
  window.pluginAction = function (action, name, btn) {
    if (action === "uninstall" && !confirm("Uninstall " + name + "? Its file and settings will be removed.")) { return; }
    showError("");
    var container = btn.closest("tr, .card");
    var errEl = container ? container.querySelector(".row-error") : null;
    if (errEl) { errEl.textContent = ""; }
    btn.disabled = true;
    var label = btn.innerHTML;
    btn.innerHTML = '<span class="spinner-border spinner-border-sm"></span> ' + label;
    var p = action === "uninstall"
      ? api("DELETE", "/api/v1/plugins/" + encodeURIComponent(name))
      : api("POST", "/api/v1/plugins/" + action, { name: name });
    p.then(loadStatus).catch(function (e) {
      btn.disabled = false; btn.innerHTML = label;
      if (errEl) { errEl.textContent = e.message; } else { showError(e.message); }
    });
  };
  window.toggleEnabled = function (name, enabled, input) {
    api("PATCH", "/api/v1/plugin-settings", [{ key: name + ".enabled", value: enabled ? "true" : "false" }])
      .catch(function (e) { input.checked = !enabled; showError(e.message); });
  };

  loadStatus();
})();
</script>
{{ template "footer.html" .}}
```

- [ ] **Step 4: Run the test, then look at it**

Run: `go test ./admin/ -run TestAdminPluginsPage -v`
Expected: PASS.

Manual check (from the repo root, fresh sqlite DB so the wizard is skipped by an existing `.env`; see README Quick Start): `ENABLE_DYNAMIC_PLUGINS=true go run .` → log in as admin → `/admin/plugins`: Installed lists analytics/socialicons/scholar/directory as built-in; Browse shows `hello` from the live directory with ★; Install → row appears as dynamic, footer greeting shows on the public site; Uninstall → gone. Run once without the env var and confirm the yellow banner + disabled Install buttons. Note anything off in the report.

- [ ] **Step 5: Commit**

```bash
git branch --show-current
git add themes/default/templates/admin_plugins.html themes/default/templates/admin_nav.html admin/plugins_test.go
git commit -m "Add the Plugins admin page with install, update and uninstall (#553)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Directory listing stars + Submit box, README, PR

**Files:**
- Modify: `plugins/directory/templates/listing.html`, `plugins/directory/directory.go` (sort), `plugins/directory/directory_test.go`
- Modify: `README.md` (Plugins feature bullets; "### Dynamic plugins" limits; new "### Installing plugins from the directory" subsection; "### Plugin directory" gets the Submit sentence)

**Interfaces:**
- Consumes: `directory.Entry.Stars` (added in Task 2).

- [ ] **Step 1: Write the failing test**

Append to `plugins/directory/directory_test.go`:

```go
func TestRenderPage_ListingSortsByStarsAndHasSubmitBox(t *testing.T) {
	srv := newFixtureServer(t)
	srv.index.Store(func(w http.ResponseWriter) {
		w.Write([]byte(`[{"name":"alpha","display_name":"Alpha","version":"1","install_type":"dynamic","stars":2},
		 {"name":"beta","display_name":"Beta","version":"1","install_type":"dynamic","stars":10},
		 {"name":"gamma","display_name":"Gamma","version":"1","install_type":"dynamic","stars":2}]`))
	})
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins", "", map[string]string{"index_url": srv.URL + "/index.json"})
	_, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	b, a, g := strings.Index(html, ">Beta<"), strings.Index(html, ">Alpha<"), strings.Index(html, ">Gamma<")
	if !(b < a && a < g) {
		t.Errorf("listing should be sorted by stars desc then name: beta=%d alpha=%d gamma=%d", b, a, g)
	}
	if !strings.Contains(html, "★ 10") {
		t.Errorf("stars should be shown, got:\n%s", html)
	}
	for _, want := range []string{`id="submit-plugin-form"`, "issues/new?template=submit-plugin.yml", "CONTRACT.md"} {
		if !strings.Contains(html, want) {
			t.Errorf("submit box missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./plugins/directory/ -run TestRenderPage_ListingSortsByStars -v`
Expected: FAIL (order and markers missing).

- [ ] **Step 3: Implement**

`plugins/directory/directory.go` — in the `ctx.SubPath == ""` branch, sort a copy of `entries` before rendering:

```go
		sorted := append([]Entry(nil), entries...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Stars != sorted[j].Stars {
				return sorted[i].Stars > sorted[j].Stars
			}
			return sorted[i].Name < sorted[j].Name
		})
		html, err := renderListing(base, sorted)
```
(add `"sort"` to the imports).

`plugins/directory/templates/listing.html` — replace the file with:

```html
<p>{{ len .Entries }} plugin{{ if ne (len .Entries) 1 }}s{{ end }} in the directory, most-starred first.
Machine-readable index: <a href="{{ .Base }}/index.json">index.json</a>.
Install them from your own goblog under <strong>Admin → Plugins</strong>.</p>
{{ if .Entries }}
<table class="table">
  <thead>
    <tr><th>Plugin</th><th>Description</th><th>Version</th><th>Author</th><th>License</th><th>Type</th><th>Stars</th></tr>
  </thead>
  <tbody>
  {{ range .Entries }}
    <tr>
      <td><a href="{{ $.Base }}/{{ .Name }}">{{ .DisplayName }}</a><br><small><a href="{{ .SourceURL }}">source</a></small></td>
      <td>{{ .Description }}</td>
      <td>{{ .Version }}</td>
      <td>{{ .Author }}</td>
      <td>{{ .License }}</td>
      <td>{{ .InstallType }}</td>
      <td>★ {{ .Stars }}</td>
    </tr>
  {{ end }}
  </tbody>
</table>
{{ end }}

<div class="card mt-4">
  <div class="card-body">
    <h2 class="h5 card-title">Submit your plugin</h2>
    <p class="card-text">A plugin is a GitHub repository with a <code>goblog-plugin.json</code> manifest, a <code>README.md</code>, a dynamic plugin file, and releases tagged <code>vX.Y.Z</code> — see the <a href="https://github.com/goblogplatform/plugins/blob/main/docs/CONTRACT.md">contract</a>. Paste your repository URL; it opens a pre-filled submission on GitHub, the registry checks the repository and opens the pull request for you.</p>
    <form id="submit-plugin-form" class="row g-2" onsubmit="return submitPlugin(this)">
      <div class="col-sm-9"><input type="url" class="form-control" name="repo" placeholder="https://github.com/you/goblog-plugin-yours" required></div>
      <div class="col-sm-3 d-grid"><button type="submit" class="btn btn-primary">Submit on GitHub</button></div>
      <div class="col-12 small text-danger" id="submit-plugin-error"></div>
    </form>
  </div>
</div>
<script>
function submitPlugin(form) {
  var err = document.getElementById("submit-plugin-error");
  var m = /^https?:\/\/(?:www\.)?github\.com\/([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/.exec(form.repo.value.trim());
  if (!m) { err.textContent = "Enter a GitHub repository URL like https://github.com/owner/repo"; return false; }
  err.textContent = "";
  var repo = m[1] + "/" + m[2];
  window.open("https://github.com/goblogplatform/plugins/issues/new?template=submit-plugin.yml&title=" + encodeURIComponent("Submit: " + repo) + "&repo=" + encodeURIComponent(repo), "_blank", "noopener");
  return false;
}
</script>
```

- [ ] **Step 4: README**

In `README.md`:

1. "### Plugins" feature bullets — add after the built-in plugins bullet:
```markdown
- Install plugins from the [directory](https://www.goblog.live/plugins) with one click under **Admin → Plugins** (dynamic plugins; needs `ENABLE_DYNAMIC_PLUGINS=true`)
```
2. Under "### Dynamic plugins", after the "Limits of the interpreted environment" list, add a new subsection before "#### Checking a plugin file":
````markdown
#### Installing from the directory
**Admin → Plugins** lists what is installed and lets you browse and search the [plugin directory](https://www.goblog.live/plugins), install a plugin with one click, update it when the directory has a newer release, or uninstall it. Requirements:
- `ENABLE_DYNAMIC_PLUGINS=true`, and `plugins/dynamic/` writable by goblog. With Docker, bind-mount that directory (as above) — otherwise installed plugins vanish with the container.
- The directory URL is the `plugin_directory_url` setting (default `https://www.goblog.live/plugins/index.json`); point it elsewhere to run a private directory.

Install downloads the plugin's `.go` file, verifies its sha256 against the directory index, loads it, checks that its name and version match, and only then writes it to `plugins/dynamic/` and starts it — no restart. Updates keep the plugin's settings; uninstall removes both. Plugins run as Go code inside goblog: install only from sources you trust.
````
3. In "### Plugin directory", append one sentence: "The directory page also has a **Submit your plugin** box: paste your repository URL and it opens a pre-filled submission on GitHub."

- [ ] **Step 5: Full verification and commit**

Run: `go build -v . && go vet ./... && go test -race goblog/... -count=1`
Expected: build ok; vet only the two known `blog/` lock-copy lines; all packages PASS.

```bash
git branch --show-current
git add plugins/directory/ README.md
git commit -m "Sort the directory by stars and add a Submit your plugin box (#553)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/553-plugin-install
gh pr create --base main --title "Admin plugin install from the directory (#553)" --body "$(cat <<'EOF'
## Summary
- **Plugin registry lifecycle**: per-plugin stop channels, `RegisterDynamic`/`Unregister`/`InitPlugin`, `Init` continues past a failing `OnInit` and returns the joined errors (closes #569).
- **`plugin/installer`**: download → sha256 → Yaegi load → name/version check → atomic write → hot register; update with rollback; uninstall removes file + settings; typed errors.
- **Admin → Plugins** page + JSON API (`/api/v1/plugins/*`): Installed tab (update available, uninstall, enable toggle) and Browse tab (search, sort by stars/name/newest, one-click Install), with banners when `ENABLE_DYNAMIC_PLUGINS` is off or the directory is unreachable.
- New site setting `plugin_directory_url` (default `https://www.goblog.live/plugins/index.json`).
- Directory page: sorted by stars, shows ★, and a **Submit your plugin** box that opens a pre-filled issue on `goblogplatform/plugins` (the registry-side workflow that turns it into a PR is a separate PR there).
- Spec: `docs/superpowers/specs/2026-09-17-plugin-install-admin-design.md`.

Follow-ups: `goblog-site-theme` needs the new `admin_plugins.html` + nav link; iac needs `ENABLE_DYNAMIC_PLUGINS=true` and a `plugins/dynamic` bind mount for goblog.live.

## Test plan
- [x] `go build -v . && go vet ./... && go test -race goblog/...`
- [x] Manual: `ENABLE_DYNAMIC_PLUGINS=true go run .`, install `hello` from the live directory, greeting appears, uninstall

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Do **not** merge the PR.

---

## Self-review notes

- Spec §1 → Task 1 (all six bullets incl. #569 and idempotent job start). §2 → Task 2 (installer flow, rollback, typed errors, `Status` shape, `DefaultIndexURL`; setting seed in Task 3). §3 → Tasks 3–4 (routes, API, page, banners, capability note, no reloads). §4 goblog half → Task 5 (stars sort/display, Submit box); registry half is the separate plan. §5 → README in Task 5; iac/site-theme are follow-ups named in the PR.
- Type consistency: `Registry.RegisterDynamic/Unregister/InitPlugin/DeleteSettings/Dynamic` + `DynamicInfo` (Task 1) are exactly what `installer.go` calls (Task 2); `installer.Status/Available/Result` and error vars (Task 2) are what `admin/plugins.go` and its tests use (Task 3); `Entry.Stars` added in Task 2 is used by Tasks 3 (test JSON), 4 (JS `p.stars`) and 5.
- Task 3's fixture uses `http://127.0.0.1/hello.go` (port-less loopback) for hello's `download_url` because the installer only allows plain HTTP to loopback; `rewritingClient` routes it to the fixture server.
