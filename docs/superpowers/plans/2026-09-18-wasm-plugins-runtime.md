# WebAssembly Plugin Runtime (goblog, sub-project A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** goblog can load, run, validate and install sandboxed WebAssembly plugins (Extism) that implement the host contract, alongside the existing compiled-in and Yaegi plugins.

**Architecture:** `plugin/wasm` wraps an Extism plugin instance in a type that implements `plugin.Plugin`, translating each hook into a JSON call of the matching export (`identity`, `settings`, `pages`, `jobs`, `template_head/footer/data`, `render_page`, `run_job`, `on_init`). goblog provides host functions `store_get/set/delete/list` (persistent per-plugin KV in a new `plugin_store` table via `plugin.Store`) and uses Extism's built-in HTTP (limited by `allowed_hosts`) and logging. A loader picks up `plugins/wasm/*.wasm` (+ `<name>.json` sidecar for allowed hosts) at boot; `validate-plugin` and the installer learn `.wasm`. A committed test fixture (`plugin/wasm/testdata/echo.wasm`, built from a tiny Go plugin with `go:generate`) drives all tests without a wasm toolchain.

**Tech Stack:** Go 1.25 module (toolchain 1.26), `github.com/extism/go-sdk` v1.7.1 (wazero), gorm/sqlite, `net/http/httptest`; fixture built with the standard toolchain (`GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`) using `github.com/extism/go-pdk` in a nested module.

**Spec:** `docs/superpowers/specs/2026-09-18-wasm-plugins-design.md` §1–2.

## Global Constraints

- Only new dependency: `github.com/extism/go-sdk v1.7.1` (brings wazero). The fixture's `go-pdk` dependency lives in the nested module `plugin/wasm/testdata/echo/go.mod`, **not** in goblog's `go.mod`.
- Host contract exactly as spec §1: export names, JSON shapes, `ctx` shape `{"settings":{},"template":"","request":{"path","sub_path","query":{},"method"}}`. `run_job` input is `{"name": "...", "settings": {...}}` (settings added so jobs can read API keys). `on_init` input is `{"settings": {...}}`.
- Host functions live in Extism's default namespace `extism:host/user`: `store_get(key) → value|empty`, `store_set(key, value)`, `store_delete(key)`, `store_list(prefix) → JSON array of keys`. Key ≤ 256 bytes, value ≤ 1 MB (`1 << 20`); violations return an error to the plugin. Logging uses Extism's built-in `pdk.Log` → goblog log prefixed `plugin <name>:`.
- Limits: `callTimeout = 10s` for `template_*`/`render_page`, `jobTimeout = 120s` for `run_job`/`on_init`; memory `1024` pages (64 MB); `MaxHttpResponseBytes = 8 MB`; one Extism instance per plugin, all calls under `sync.Mutex`.
- `identity`, `settings`, `pages`, `jobs` are called once at load and cached; missing optional exports behave as `BasePlugin` no-ops; only `identity` is mandatory.
- If the plugin declares an `enabled` setting and its value is not `"true"`, the adapter returns no-ops for `TemplateHead/Footer/Data`, skips `run_job`, and `RenderPage` is never reached (registry already filters disabled page plugins).
- `render_page` output: `{"html":…}` → `("page_content.html", {"has_plugin_content":true,"plugin_content":html})`; `{"template":…,"data":{…}}` → passed through; `{"raw":{"status","content_type","body"}}` → written directly, `("", nil)`; anything else/empty → `("", nil)`.
- Loader: `plugins/wasm/*.wasm`; sidecar `plugins/wasm/<base>.json` `{"allowed_hosts":[…]}`; enabled unless `ENABLE_WASM_PLUGINS=false`.
- `DynamicInfo.Runtime` = `"wasm"` when `Path` ends in `.wasm`, else `"go"`.
- `validate-plugin` prints `{"name","display_name","version","runtime":"wasm"}` for `.wasm`; `.go` output unchanged (no `runtime` key).
- Installer: only `install_type == "wasm"` is installable from the directory (`ErrNotDynamic` text becomes "this plugin type cannot be installed from the directory"); target `WasmDir/<name>.wasm` + sidecar; download cap 16 MB for wasm.
- `directory.Entry` gains `Runtime string \`json:"runtime"\`` and `AllowedHosts []string \`json:"allowed_hosts"\``.
- Commit messages: imperative, trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`. Branch `feat/wasm-plugins`; never push to `main`; never merge; `git branch --show-current` before every commit.
- Verify with `go build -v . && go vet ./... && go test -race goblog/...` (two pre-existing `go vet` lock-copy lines in `blog/` are known).

---

## File structure

| File | Responsibility |
|---|---|
| `plugin/store.go` (+`store_test.go`) | `Store` interface, `PluginStoreEntry` model, `Registry.Store()` gorm implementation |
| `plugin/wasm/testdata/echo/{go.mod,main.go}` | fixture plugin source (nested module) |
| `plugin/wasm/testdata/echo.wasm` | committed fixture artifact |
| `plugin/wasm/generate.go` | `//go:generate` that rebuilds the fixture |
| `plugin/wasm/contract.go` | JSON types of the host contract |
| `plugin/wasm/host.go` | store host functions |
| `plugin/wasm/wasm.go` (+`wasm_test.go`) | `Options`, `Load`, `LoadBytes`, `*Plugin` implementing `plugin.Plugin` |
| `plugin/wasm/loader.go` (+`loader_test.go`) | `LoadWasmPlugins(registry, dir, store)`, sidecar handling |
| `plugin/registry.go` | `DynamicInfo.Runtime` |
| `cmd_validate.go` (+test), `plugin/validate.go` | `.wasm` validation, `Info.Runtime` |
| `goblog.go` | store wiring, wasm loader, installer `WasmDir` |
| `plugins/directory/index.go` | `Runtime`, `AllowedHosts` |
| `plugin/installer/installer.go` (+test) | wasm install/update/uninstall |
| `admin/plugins.go`, `themes/default/templates/admin_plugins.html` | runtime badge, hosts, copy |
| `README.md` | docs |

---

### Task 1: `plugin.Store` and the `plugin_store` table

**Files:**
- Create: `plugin/store.go`, `plugin/store_test.go`
- Modify: `plugin/registry.go` (`Init`/`InitPlugin` migrate the new model; add `Store()`)

**Interfaces:**
- Produces:
  ```go
  type Store interface {
      Get(pluginName, key string) (value []byte, found bool, err error)
      Set(pluginName, key string, value []byte) error
      Delete(pluginName, key string) error
      List(pluginName, prefix string) ([]string, error)
      DeleteAll(pluginName string) error
  }
  type PluginStoreEntry struct { PluginName string `gorm:"primaryKey;size:64"`; Key string `gorm:"primaryKey;size:256"`; Value []byte; UpdatedAt time.Time }
  func (PluginStoreEntry) TableName() string { return "plugin_store" }
  func (r *Registry) Store() Store        // gorm-backed; errors with ErrStoreUnavailable when db is nil
  var ErrStoreUnavailable = errors.New("plugin store: database not ready")
  const MaxStoreKeyBytes = 256; const MaxStoreValueBytes = 1 << 20
  ```
  Task 2 (host functions), Task 3 (loader) and Task 5 (installer) use `Registry.Store()`; `Uninstall` calls `DeleteAll`.

- [ ] **Step 1: Failing test** — `plugin/store_test.go`:

```go
package plugin_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"goblog/plugin"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStore_RoundTrip(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	s := reg.Store()

	if _, found, err := s.Get("a", "missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v", found, err)
	}
	if err := s.Set("a", "k1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", "k2", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b", "k1", []byte("other")); err != nil {
		t.Fatal(err)
	}
	v, found, err := s.Get("a", "k1")
	if err != nil || !found || !bytes.Equal(v, []byte("v1")) {
		t.Errorf("get a/k1 = %q %v %v", v, found, err)
	}
	if err := s.Set("a", "k1", []byte("v1b")); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get("a", "k1"); string(v) != "v1b" {
		t.Errorf("overwrite failed: %q", v)
	}
	keys, err := s.List("a", "k")
	if err != nil || len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Errorf("list = %v %v", keys, err)
	}
	if keys, _ := s.List("a", "k2"); len(keys) != 1 {
		t.Errorf("prefix list = %v", keys)
	}
	if err := s.Delete("a", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get("a", "k1"); found {
		t.Error("k1 should be deleted")
	}
	if v, _, _ := s.Get("b", "k1"); string(v) != "other" {
		t.Error("plugins must be isolated")
	}
	if err := s.DeleteAll("a"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.List("a", ""); len(keys) != 0 {
		t.Errorf("DeleteAll left %v", keys)
	}
	if v, _, _ := s.Get("b", "k1"); string(v) != "other" {
		t.Error("DeleteAll must not touch other plugins")
	}
}

func TestStore_Limits(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	reg.Init()
	s := reg.Store()
	if err := s.Set("a", strings.Repeat("k", plugin.MaxStoreKeyBytes+1), []byte("v")); err == nil {
		t.Error("oversized key should be rejected")
	}
	if err := s.Set("a", "k", make([]byte, plugin.MaxStoreValueBytes+1)); err == nil {
		t.Error("oversized value should be rejected")
	}
	if err := s.Set("a", "", []byte("v")); err == nil {
		t.Error("empty key should be rejected")
	}
	if err := s.Set("a", "k", make([]byte, plugin.MaxStoreValueBytes)); err != nil {
		t.Errorf("max-size value should be accepted: %v", err)
	}
}

func TestStore_NoDB(t *testing.T) {
	reg := plugin.NewRegistry(nil)
	s := reg.Store()
	if _, _, err := s.Get("a", "k"); !errors.Is(err, plugin.ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable, got %v", err)
	}
	if err := s.Set("a", "k", []byte("v")); !errors.Is(err, plugin.ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable, got %v", err)
	}
}
```

- [ ] **Step 2: Run** `go test ./plugin/ -run TestStore -v` → FAIL to compile (`reg.Store undefined`).

- [ ] **Step 3: Implement** `plugin/store.go`:

```go
package plugin

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store is the persistent key/value storage a plugin gets through the
// store_* host functions. Keys are namespaced by plugin name.
type Store interface {
	Get(pluginName, key string) (value []byte, found bool, err error)
	Set(pluginName, key string, value []byte) error
	Delete(pluginName, key string) error
	List(pluginName, prefix string) ([]string, error)
	DeleteAll(pluginName string) error
}

const (
	MaxStoreKeyBytes   = 256
	MaxStoreValueBytes = 1 << 20
)

// ErrStoreUnavailable is returned before the database is configured (wizard).
var ErrStoreUnavailable = errors.New("plugin store: database not ready")

// PluginStoreEntry is one row of the plugin_store table.
type PluginStoreEntry struct {
	PluginName string `gorm:"primaryKey;size:64"`
	Key        string `gorm:"primaryKey;size:256"`
	Value      []byte
	UpdatedAt  time.Time
}

func (PluginStoreEntry) TableName() string { return "plugin_store" }

// Store returns the registry's database-backed Store. It reads the current
// db on every call, so it keeps working after the wizard sets one.
func (r *Registry) Store() Store { return &dbStore{r: r} }

type dbStore struct{ r *Registry }

func (s *dbStore) db() (*gorm.DB, error) {
	s.r.mu.RLock()
	defer s.r.mu.RUnlock()
	if s.r.db == nil {
		return nil, ErrStoreUnavailable
	}
	return s.r.db, nil
}

func (s *dbStore) Get(pluginName, key string) ([]byte, bool, error) {
	db, err := s.db()
	if err != nil {
		return nil, false, err
	}
	var e PluginStoreEntry
	err = db.Where("plugin_name = ? AND key = ?", pluginName, key).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return e.Value, true, nil
}

func (s *dbStore) Set(pluginName, key string, value []byte) error {
	if key == "" || len(key) > MaxStoreKeyBytes {
		return fmt.Errorf("plugin store: key must be 1-%d bytes", MaxStoreKeyBytes)
	}
	if len(value) > MaxStoreValueBytes {
		return fmt.Errorf("plugin store: value exceeds %d bytes", MaxStoreValueBytes)
	}
	db, err := s.db()
	if err != nil {
		return err
	}
	e := PluginStoreEntry{PluginName: pluginName, Key: key, Value: value, UpdatedAt: time.Now()}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "plugin_name"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&e).Error
}

func (s *dbStore) Delete(pluginName, key string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Where("plugin_name = ? AND key = ?", pluginName, key).Delete(&PluginStoreEntry{}).Error
}

func (s *dbStore) List(pluginName, prefix string) ([]string, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	var keys []string
	q := db.Model(&PluginStoreEntry{}).Where("plugin_name = ?", pluginName)
	if prefix != "" {
		q = q.Where("key LIKE ?", escapeLike(prefix)+"%")
	}
	if err := q.Order("key asc").Pluck("key", &keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func (s *dbStore) DeleteAll(pluginName string) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	return db.Where("plugin_name = ?", pluginName).Delete(&PluginStoreEntry{}).Error
}

// escapeLike escapes LIKE wildcards so a prefix is matched literally.
func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' || s[i] == '_' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
```

SQLite needs `ESCAPE '\'` for the escaped LIKE: use `q.Where("key LIKE ? ESCAPE '\\'", escapeLike(prefix)+"%")`.

In `plugin/registry.go`, wherever `db.AutoMigrate(&PluginSetting{})` is called (`Init` and `InitPlugin`), migrate both: `db.AutoMigrate(&PluginSetting{}, &PluginStoreEntry{})`.

- [ ] **Step 4: Run** `go test -race ./plugin/ -count=1` → PASS.
- [ ] **Step 5: Commit** — `git add plugin/store.go plugin/store_test.go plugin/registry.go && git commit -m "Add a per-plugin persistent key/value store

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"`

---

### Task 2: Fixture plugin, contract types, adapter (`plugin/wasm`)

**Files:**
- Create: `plugin/wasm/testdata/echo/go.mod`, `plugin/wasm/testdata/echo/main.go`, `plugin/wasm/testdata/echo.wasm` (built), `plugin/wasm/generate.go`, `plugin/wasm/contract.go`, `plugin/wasm/host.go`, `plugin/wasm/wasm.go`, `plugin/wasm/wasm_test.go`
- Modify: `go.mod`/`go.sum` (`go get github.com/extism/go-sdk@v1.7.1`)

**Interfaces:**
- Consumes: `plugin.Store`, `plugin.Plugin`, `plugin.HookContext` (with `SubPath`), `plugin.SettingDefinition/PageDefinition/ScheduledJob`.
- Produces (package `wasm`):
  ```go
  type Options struct { Store plugin.Store; AllowedHosts []string; Logf func(format string, args ...any) }
  func Load(path string, opts Options) (*Plugin, error)
  func LoadBytes(data []byte, opts Options) (*Plugin, error)
  type Plugin struct{ … } // implements plugin.Plugin; also Close() error, Identity() Identity, AllowedHosts() []string
  type Identity struct { Name, DisplayName, Version string }
  var ErrNoIdentity = errors.New("wasm plugin: missing identity export")
  ```

- [ ] **Step 1: Fixture plugin**

`plugin/wasm/testdata/echo/go.mod`:
```
module echo

go 1.25

require github.com/extism/go-pdk v1.1.3
```
(`go mod tidy` inside that directory fills `go.sum`.)

`plugin/wasm/testdata/echo/main.go`:
```go
// echo is the goblog wasm test fixture: it implements every export of the
// host contract in the most literal way so the adapter can be tested.
package main

import (
	"encoding/json"
	"strings"

	pdk "github.com/extism/go-pdk"
)

//go:wasmimport extism:host/user store_get
func hostStoreGet(uint64) uint64

//go:wasmimport extism:host/user store_set
func hostStoreSet(uint64, uint64) uint64

//go:wasmimport extism:host/user store_delete
func hostStoreDelete(uint64) uint64

//go:wasmimport extism:host/user store_list
func hostStoreList(uint64) uint64

func storeGet(key string) string {
	k := pdk.AllocateString(key)
	defer k.Free()
	return string(pdk.FindMemory(hostStoreGet(k.Offset())).ReadBytes())
}

func storeSet(key, value string) {
	k := pdk.AllocateString(key)
	defer k.Free()
	v := pdk.AllocateString(value)
	defer v.Free()
	hostStoreSet(k.Offset(), v.Offset())
}

func storeDelete(key string) {
	k := pdk.AllocateString(key)
	defer k.Free()
	hostStoreDelete(k.Offset())
}

func storeList(prefix string) string {
	p := pdk.AllocateString(prefix)
	defer p.Free()
	return string(pdk.FindMemory(hostStoreList(p.Offset())).ReadBytes())
}

type ctx struct {
	Settings map[string]string `json:"settings"`
	Template string            `json:"template"`
	Request  struct {
		Path    string            `json:"path"`
		SubPath string            `json:"sub_path"`
		Query   map[string]string `json:"query"`
		Method  string            `json:"method"`
	} `json:"request"`
}

func readCtx() ctx {
	var c ctx
	json.Unmarshal(pdk.Input(), &c)
	return c
}

func out(v any) int32 {
	b, _ := json.Marshal(v)
	pdk.Output(b)
	return 0
}

//go:wasmexport identity
func identity() int32 {
	return out(map[string]string{"name": "echo", "display_name": "Echo", "version": "1.2.3"})
}

//go:wasmexport settings
func settings() int32 {
	return out([]map[string]string{
		{"key": "enabled", "type": "text", "default": "true", "label": "Enabled", "description": "on/off"},
		{"key": "greeting", "type": "text", "default": "hi", "label": "Greeting", "description": "footer text"},
	})
}

//go:wasmexport pages
func pages() int32 {
	return out([]map[string]any{{"page_type": "echo", "title": "Echo", "slug": "echo", "show_in_nav": true, "nav_order": 5, "description": "echo page"}})
}

//go:wasmexport jobs
func jobs() int32 {
	return out([]map[string]any{{"name": "tick", "interval_seconds": 60}})
}

//go:wasmexport template_footer
func templateFooter() int32 {
	c := readCtx()
	pdk.OutputString("<p>" + c.Settings["greeting"] + "</p>")
	return 0
}

//go:wasmexport template_head
func templateHead() int32 {
	pdk.OutputString("<!-- echo head -->")
	return 0
}

//go:wasmexport template_data
func templateData() int32 {
	c := readCtx()
	return out(map[string]any{"greeting": c.Settings["greeting"], "template": c.Template})
}

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
	case "store":
		storeSet("k", "v-"+c.Request.Query["v"])
		got := storeGet("k")
		keys := storeList("")
		storeDelete("k")
		after := storeGet("k")
		return out(map[string]any{"html": "got=" + got + " keys=" + keys + " after=[" + after + "]"})
	case "http":
		req := pdk.NewHTTPRequest(pdk.MethodGet, c.Request.Query["url"])
		resp := req.Send()
		return out(map[string]any{"html": "status=" + itoa(int(resp.Status())) + " body=" + string(resp.Body())})
	case "log":
		pdk.Log(pdk.LogInfo, "hello from echo")
		return out(map[string]any{"html": "logged"})
	case "spin":
		for {
		}
	case "big":
		b := make([]byte, 200<<20)
		return out(map[string]any{"html": itoa(len(b))})
	case "boom":
		pdk.SetError("kaboom")
		return 1
	}
	return out(map[string]any{})
}

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

//go:wasmexport on_init
func onInit() int32 {
	storeSet("init", "1")
	return out(map[string]any{})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return strings.TrimLeft(string(b), " ")
}

func main() {}
```

`plugin/wasm/generate.go`:
```go
package wasm

// The test fixture is built with the standard Go toolchain and committed so
// `go test` needs no wasm toolchain. Rebuild after editing testdata/echo:
//
//go:generate sh -c "cd testdata/echo && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../echo.wasm ."
```

Build it: `cd plugin/wasm/testdata/echo && go mod tidy && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../echo.wasm . && ls -la ../echo.wasm` (expect ~2–4 MB; if `go build` reports `-buildmode=c-shared not supported`, the toolchain is too old — it needs Go ≥ 1.24; use `GOTOOLCHAIN=go1.26.1`).

- [ ] **Step 2: Failing tests** — `plugin/wasm/wasm_test.go`:

```go
package wasm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// memStore is an in-memory plugin.Store.
type memStore struct {
	mu sync.Mutex
	m  map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string]map[string][]byte{}} }
func (s *memStore) Get(p, k string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[p][k]
	return v, ok, nil
}
func (s *memStore) Set(p, k string, v []byte) error {
	if len(k) == 0 || len(k) > plugin.MaxStoreKeyBytes || len(v) > plugin.MaxStoreValueBytes {
		return errors.New("limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[p] == nil {
		s.m[p] = map[string][]byte{}
	}
	s.m[p][k] = append([]byte(nil), v...)
	return nil
}
func (s *memStore) Delete(p, k string) error { s.mu.Lock(); defer s.mu.Unlock(); delete(s.m[p], k); return nil }
func (s *memStore) List(p, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.m[p] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *memStore) DeleteAll(p string) error { s.mu.Lock(); defer s.mu.Unlock(); delete(s.m, p); return nil }

func echoBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/echo.wasm")
	if err != nil {
		t.Fatalf("fixture missing (run go generate ./plugin/wasm): %v", err)
	}
	return b
}

func loadEcho(t *testing.T, opts Options) *Plugin {
	t.Helper()
	if opts.Store == nil {
		opts.Store = newMemStore()
	}
	p, err := LoadBytes(echoBytes(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func hookCtx(settings map[string]string, path, subPath string) *plugin.HookContext {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return &plugin.HookContext{GinContext: c, Settings: settings, Template: "post.html", SubPath: subPath}
}

func TestLoad_IdentitySettingsPagesJobs(t *testing.T) {
	p := loadEcho(t, Options{})
	if p.Name() != "echo" || p.DisplayName() != "Echo" || p.Version() != "1.2.3" {
		t.Errorf("identity = %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	s := p.Settings()
	if len(s) != 2 || s[0].Key != "enabled" || s[0].DefaultValue != "true" || s[1].Key != "greeting" || s[1].Label != "Greeting" {
		t.Errorf("settings = %+v", s)
	}
	pg := p.Pages()
	if len(pg) != 1 || pg[0].PageType != "echo" || pg[0].Slug != "echo" || !pg[0].ShowInNav || pg[0].NavOrder != 5 {
		t.Errorf("pages = %+v", pg)
	}
	j := p.ScheduledJobs()
	if len(j) != 1 || j[0].Name != "tick" || j[0].Interval != 60*time.Second {
		t.Errorf("jobs = %+v", j)
	}
	var _ plugin.Plugin = p
}

func TestLoad_RejectsNonPluginAndMissingIdentity(t *testing.T) {
	if _, err := LoadBytes([]byte("not wasm"), Options{}); err == nil {
		t.Error("garbage should fail to load")
	}
	// A valid module without the identity export: use a minimal hand-written wasm.
	empty := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00} // "\0asm" v1, no sections
	if _, err := LoadBytes(empty, Options{}); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("expected ErrNoIdentity, got %v", err)
	}
}

func TestHooks(t *testing.T) {
	p := loadEcho(t, Options{})
	enabled := map[string]string{"enabled": "true", "greeting": "hello"}
	if got := p.TemplateFooter(hookCtx(enabled, "/", "")); got != "<p>hello</p>" {
		t.Errorf("footer = %q", got)
	}
	if got := p.TemplateHead(hookCtx(enabled, "/", "")); got != "<!-- echo head -->" {
		t.Errorf("head = %q", got)
	}
	data := p.TemplateData(hookCtx(enabled, "/", ""))
	if data["greeting"] != "hello" || data["template"] != "post.html" {
		t.Errorf("data = %v", data)
	}
	disabled := map[string]string{"enabled": "false", "greeting": "hello"}
	if p.TemplateFooter(hookCtx(disabled, "/", "")) != "" || p.TemplateHead(hookCtx(disabled, "/", "")) != "" || p.TemplateData(hookCtx(disabled, "/", "")) != nil {
		t.Error("disabled plugin hooks must be no-ops")
	}
}

func TestRenderPage(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true", "greeting": "yo"}

	tmpl, data := p.RenderPage(hookCtx(settings, "/echo", ""), "echo")
	if tmpl != "page_content.html" || data["has_plugin_content"] != true || data["plugin_content"] != "<h2>echo</h2>yo" {
		t.Errorf("html: %q %v", tmpl, data)
	}
	tmpl, data = p.RenderPage(hookCtx(settings, "/echo/tmpl", "tmpl"), "echo")
	if tmpl != "page_content.html" || data["plugin_content"] != "from template" {
		t.Errorf("template: %q %v", tmpl, data)
	}
	ctx := hookCtx(settings, "/echo/data.json", "data.json")
	tmpl, _ = p.RenderPage(ctx, "echo")
	w := ctx.GinContext.Writer.(interface{ Status() int })
	if tmpl != "" || w.Status() != 200 || !ctx.GinContext.Writer.Written() {
		t.Errorf("raw: tmpl=%q status=%d written=%v", tmpl, w.Status(), ctx.GinContext.Writer.Written())
	}
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/nope", "nope"), "echo"); tmpl != "" {
		t.Errorf("unknown sub-path should be declined, got %q", tmpl)
	}
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "other"); tmpl != "" {
		t.Errorf("other page type should be declined, got %q", tmpl)
	}
	// Plugin error → declined, not a panic.
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/boom", "boom"), "echo"); tmpl != "" {
		t.Errorf("plugin error should decline, got %q", tmpl)
	}
}

func TestStoreHostFunctions(t *testing.T) {
	st := newMemStore()
	p := loadEcho(t, Options{Store: st})
	settings := map[string]string{"enabled": "true"}
	ctx := hookCtx(settings, "/echo/store?v=1", "store")
	_, data := p.RenderPage(ctx, "echo")
	html, _ := data["plugin_content"].(string)
	if html != `got=v-1 keys=["k"] after=[]` {
		t.Errorf("store round trip = %q", html)
	}
	// Namespaced by plugin name.
	if _, found, _ := st.Get("echo", "k"); found {
		t.Error("k should have been deleted")
	}
	// Jobs and on_init reach the store with the plugin's settings.
	if err := p.OnInit(nil); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "init"); string(v) != "1" {
		t.Error("on_init should have written init=1")
	}
	if err := p.ScheduledJobs()[0].Run((*gorm.DB)(nil), map[string]string{"enabled": "true", "greeting": "g"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "job_ran"); string(v) != "tick/g" {
		t.Errorf("job_ran = %q", v)
	}
	if err := p.ScheduledJobs()[0].Run((*gorm.DB)(nil), map[string]string{"enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	// Disabled: job must not run (value unchanged).
	if v, _, _ := st.Get("echo", "job_ran"); string(v) != "tick/g" {
		t.Error("disabled plugin's job must not run")
	}
}

func TestHTTPAllowedHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("pong")) }))
	defer srv.Close()
	settings := map[string]string{"enabled": "true"}

	allowed := loadEcho(t, Options{AllowedHosts: []string{"127.0.0.1"}})
	_, data := allowed.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+"/ping", "http"), "echo")
	if html, _ := data["plugin_content"].(string); html != "status=200 body=pong" {
		t.Errorf("allowed host: %q", html)
	}
	denied := loadEcho(t, Options{})
	if tmpl, _ := denied.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+"/ping", "http"), "echo"); tmpl != "" {
		t.Errorf("no allowed hosts → request must fail and the page decline, got %q", tmpl)
	}
}

func TestTimeoutAndMemoryCap(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true"}
	old := callTimeout
	callTimeout = 300 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	start := time.Now()
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/spin", "spin"), "echo"); tmpl != "" {
		t.Error("spinning plugin should be interrupted and decline")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout did not interrupt the plugin")
	}
	// After a timeout the instance is unusable; the adapter must report that
	// clearly rather than hang or panic.
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "" {
		t.Log("instance recovered after timeout (acceptable)")
	}
	p2 := loadEcho(t, Options{})
	if tmpl, _ := p2.RenderPage(hookCtx(settings, "/echo/big", "big"), "echo"); tmpl != "" {
		t.Error("200 MB allocation should exceed the memory cap and decline")
	}
}

func TestLogging(t *testing.T) {
	var logged []string
	p := loadEcho(t, Options{Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	p.RenderPage(hookCtx(map[string]string{"enabled": "true"}, "/echo/log", "log"), "echo")
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "plugin echo") || !strings.Contains(joined, "hello from echo") {
		t.Errorf("expected the plugin's log line prefixed with its name, got %q", joined)
	}
}
```

(Imports for this file: `errors`, `fmt`, `net/http`, `net/http/httptest`, `os`, `strings`, `sync`, `testing`, `time`, `goblog/plugin`, gin, gorm — drop `context`.)

- [ ] **Step 3: Run** `go test ./plugin/wasm/ -v` → FAIL to compile (`undefined: LoadBytes`).

- [ ] **Step 4: Implement**

`go get github.com/extism/go-sdk@v1.7.1`.

`plugin/wasm/contract.go`:
```go
package wasm

// JSON shapes of the host contract (spec §1). Field names are the API.

type Identity struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
}

type settingDef struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type pageDef struct {
	PageType    string `json:"page_type"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	ShowInNav   bool   `json:"show_in_nav"`
	NavOrder    int    `json:"nav_order"`
	Description string `json:"description"`
}

type jobDef struct {
	Name            string `json:"name"`
	IntervalSeconds int    `json:"interval_seconds"`
}

type requestCtx struct {
	Path    string            `json:"path"`
	SubPath string            `json:"sub_path"`
	Query   map[string]string `json:"query"`
	Method  string            `json:"method"`
}

type hookCtx struct {
	Settings map[string]string `json:"settings"`
	Template string            `json:"template"`
	Request  requestCtx        `json:"request"`
}

type jobInput struct {
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings"`
}

type initInput struct {
	Settings map[string]string `json:"settings"`
}

type rawResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

type renderResult struct {
	HTML     *string        `json:"html"`
	Template string         `json:"template"`
	Data     map[string]any `json:"data"`
	Raw      *rawResponse   `json:"raw"`
}
```

`plugin/wasm/host.go`:
```go
package wasm

import (
	"context"
	"encoding/json"

	"goblog/plugin"

	extism "github.com/extism/go-sdk"
)

// hostFunctions builds the store_* host functions. They read the plugin's
// name through p at call time, because the name is only known after the
// identity export has run.
func hostFunctions(p *Plugin) []extism.HostFunction {
	i64 := []extism.ValueType{extism.ValueTypeI64}
	fail := func(cp *extism.CurrentPlugin, err error) uint64 {
		cp.Log(extism.LogLevelWarn, "store: "+err.Error())
		off, _ := cp.WriteString("")
		return off
	}
	get := extism.NewHostFunctionWithStack("store_get", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err := cp.ReadString(stack[0])
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		v, _, err := p.opts.Store.Get(p.name, key)
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		stack[0], _ = cp.WriteBytes(v)
	}, i64, i64)
	set := extism.NewHostFunctionWithStack("store_set", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err1 := cp.ReadString(stack[0])
		val, err2 := cp.ReadBytes(stack[1])
		if err1 != nil || err2 != nil {
			stack[0] = 1
			return
		}
		if err := p.opts.Store.Set(p.name, key, val); err != nil {
			cp.Log(extism.LogLevelWarn, "store_set: "+err.Error())
			stack[0] = 1
			return
		}
		stack[0] = 0
	}, []extism.ValueType{extism.ValueTypeI64, extism.ValueTypeI64}, i64)
	del := extism.NewHostFunctionWithStack("store_delete", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err := cp.ReadString(stack[0])
		if err != nil || p.opts.Store.Delete(p.name, key) != nil {
			stack[0] = 1
			return
		}
		stack[0] = 0
	}, i64, i64)
	list := extism.NewHostFunctionWithStack("store_list", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		prefix, err := cp.ReadString(stack[0])
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		keys, err := p.opts.Store.List(p.name, prefix)
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		if keys == nil {
			keys = []string{}
		}
		b, _ := json.Marshal(keys)
		stack[0], _ = cp.WriteBytes(b)
	}, i64, i64)
	return []extism.HostFunction{get, set, del, list}
}

// nopStore is used when no Store is configured (validation): every write
// fails, every read is empty.
type nopStore struct{}

func (nopStore) Get(string, string) ([]byte, bool, error) { return nil, false, nil }
func (nopStore) Set(string, string, []byte) error           { return plugin.ErrStoreUnavailable }
func (nopStore) Delete(string, string) error                { return plugin.ErrStoreUnavailable }
func (nopStore) List(string, string) ([]string, error)      { return nil, nil }
func (nopStore) DeleteAll(string) error                     { return plugin.ErrStoreUnavailable }
```

`plugin/wasm/wasm.go`:
```go
// Package wasm runs WebAssembly plugins (Extism) behind the plugin.Plugin
// interface. Each hook becomes a JSON call of the matching export; plugins
// get persistent storage through store_* host functions and HTTP through
// Extism's built-in client, limited to their allowed hosts.
package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"goblog/plugin"

	extism "github.com/extism/go-sdk"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Options configure a loaded plugin.
type Options struct {
	Store        plugin.Store                  // nil → writes fail, reads are empty
	AllowedHosts []string                      // hosts the plugin may reach over HTTP; nil → none
	Logf         func(format string, args ...any) // nil → log.Printf
}

var (
	callTimeout = 10 * time.Second  // template_* and render_page
	jobTimeout  = 120 * time.Second // run_job and on_init
)

const (
	memoryPages          = 1024    // 64 MB
	maxHTTPResponseBytes = 8 << 20 // 8 MB
)

// ErrNoIdentity is returned when a module has no identity export.
var ErrNoIdentity = errors.New("wasm plugin: missing identity export")

// Plugin is a loaded WebAssembly plugin. It implements plugin.Plugin.
type Plugin struct {
	mu       sync.Mutex
	ext      *extism.Plugin
	opts     Options
	name     string
	identity Identity
	settings []plugin.SettingDefinition
	pages    []plugin.PageDefinition
	jobs     []jobDef
	hasEnabledSetting bool
}

// Load reads a .wasm file and instantiates it.
func Load(path string, opts Options) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := LoadBytes(data, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// LoadBytes instantiates a plugin from module bytes and reads its identity,
// settings, pages and jobs once.
func LoadBytes(data []byte, opts Options) (*Plugin, error) {
	if opts.Store == nil {
		opts.Store = nopStore{}
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	p := &Plugin{opts: opts}
	manifest := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: data}},
		AllowedHosts: opts.AllowedHosts,
		Memory:       &extism.ManifestMemory{MaxPages: memoryPages, MaxHttpResponseBytes: maxHTTPResponseBytes},
		Timeout:      uint64(callTimeout / time.Millisecond),
	}
	ext, err := extism.NewPlugin(context.Background(), manifest, extism.PluginConfig{EnableWasi: true}, hostFunctions(p))
	if err != nil {
		return nil, fmt.Errorf("wasm plugin: %w", err)
	}
	p.ext = ext
	ext.SetLogger(func(level extism.LogLevel, msg string) {
		p.opts.Logf("plugin %s: %s: %s", p.displayForLog(), level, msg)
	})
	if !ext.FunctionExists("identity") {
		ext.Close(context.Background())
		return nil, ErrNoIdentity
	}
	if err := p.callJSON("identity", nil, &p.identity, callTimeout); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin: identity: %w", err)
	}
	if p.identity.Name == "" || p.identity.Version == "" {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin: identity must include name and version")
	}
	p.name = p.identity.Name

	var defs []settingDef
	if err := p.optionalJSON("settings", nil, &defs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: settings: %w", p.name, err)
	}
	for _, d := range defs {
		if d.Key == "enabled" {
			p.hasEnabledSetting = true
		}
		p.settings = append(p.settings, plugin.SettingDefinition{Key: d.Key, Type: d.Type, DefaultValue: d.Default, Label: d.Label, Description: d.Description})
	}
	var pgs []pageDef
	if err := p.optionalJSON("pages", nil, &pgs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: pages: %w", p.name, err)
	}
	for _, d := range pgs {
		p.pages = append(p.pages, plugin.PageDefinition{PageType: d.PageType, Title: d.Title, Slug: d.Slug, ShowInNav: d.ShowInNav, NavOrder: d.NavOrder, Description: d.Description})
	}
	if err := p.optionalJSON("jobs", nil, &p.jobs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: jobs: %w", p.name, err)
	}
	return p, nil
}

func (p *Plugin) displayForLog() string {
	if p.name != "" {
		return p.name
	}
	return "(loading)"
}

// Close releases the instance.
func (p *Plugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ext.Close(context.Background())
}

// Identity returns what the plugin reported.
func (p *Plugin) Identity() Identity { return p.identity }

// AllowedHosts returns the hosts this instance may reach.
func (p *Plugin) AllowedHosts() []string { return p.opts.AllowedHosts }

// call runs an export under the mutex with the given timeout. A non-zero
// exit or a runtime error is returned as error; output is the raw bytes.
func (p *Plugin) call(name string, input []byte, timeout time.Duration) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ext.Timeout = timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	code, out, err := p.ext.CallWithContext(ctx, name, input)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s returned %d: %s", name, code, p.ext.GetError())
	}
	return out, nil
}

func (p *Plugin) callJSON(name string, input any, out any, timeout time.Duration) error {
	var in []byte
	if input != nil {
		var err error
		if in, err = json.Marshal(input); err != nil {
			return err
		}
	}
	b, err := p.call(name, in, timeout)
	if err != nil {
		return err
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: invalid JSON output: %w", name, err)
	}
	return nil
}

// optionalJSON is callJSON for exports that may be absent.
func (p *Plugin) optionalJSON(name string, input any, out any) error {
	if !p.ext.FunctionExists(name) {
		return nil
	}
	return p.callJSON(name, input, out, callTimeout)
}

func (p *Plugin) enabled(settings map[string]string) bool {
	return !p.hasEnabledSetting || settings["enabled"] == "true"
}

func makeCtx(ctx *plugin.HookContext) hookCtx {
	hc := hookCtx{Settings: ctx.Settings, Template: ctx.Template}
	if hc.Settings == nil {
		hc.Settings = map[string]string{}
	}
	if ctx.GinContext != nil && ctx.GinContext.Request != nil {
		r := ctx.GinContext.Request
		hc.Request = requestCtx{Path: r.URL.Path, SubPath: ctx.SubPath, Method: r.Method, Query: map[string]string{}}
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				hc.Request.Query[k] = v[0]
			}
		}
	} else {
		hc.Request.SubPath = ctx.SubPath
		hc.Request.Query = map[string]string{}
	}
	return hc
}

// --- plugin.Plugin ---

func (p *Plugin) Name() string                             { return p.identity.Name }
func (p *Plugin) DisplayName() string                      { return p.identity.DisplayName }
func (p *Plugin) Version() string                          { return p.identity.Version }
func (p *Plugin) Settings() []plugin.SettingDefinition     { return p.settings }
func (p *Plugin) Pages() []plugin.PageDefinition           { return p.pages }

func (p *Plugin) ScheduledJobs() []plugin.ScheduledJob {
	jobs := make([]plugin.ScheduledJob, 0, len(p.jobs))
	for _, j := range p.jobs {
		name := j.Name
		interval := time.Duration(j.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Hour
		}
		jobs = append(jobs, plugin.ScheduledJob{Name: name, Interval: interval, Run: func(_ *gorm.DB, settings map[string]string) error {
			if !p.enabled(settings) {
				return nil
			}
			if err := p.callJSON("run_job", jobInput{Name: name, Settings: settings}, nil, jobTimeout); err != nil {
				return fmt.Errorf("wasm plugin %s job %s: %w", p.name, name, err)
			}
			return nil
		}})
	}
	return jobs
}

func (p *Plugin) stringHook(name string, ctx *plugin.HookContext) string {
	if !p.enabled(ctx.Settings) || !p.ext.FunctionExists(name) {
		return ""
	}
	in, _ := json.Marshal(makeCtx(ctx))
	out, err := p.call(name, in, callTimeout)
	if err != nil {
		p.opts.Logf("plugin %s: %s: %v", p.name, name, err)
		return ""
	}
	return string(out)
}

func (p *Plugin) TemplateHead(ctx *plugin.HookContext) string   { return p.stringHook("template_head", ctx) }
func (p *Plugin) TemplateFooter(ctx *plugin.HookContext) string { return p.stringHook("template_footer", ctx) }

func (p *Plugin) TemplateData(ctx *plugin.HookContext) gin.H {
	if !p.enabled(ctx.Settings) || !p.ext.FunctionExists("template_data") {
		return nil
	}
	var data map[string]any
	if err := p.callJSON("template_data", makeCtx(ctx), &data, callTimeout); err != nil {
		p.opts.Logf("plugin %s: template_data: %v", p.name, err)
		return nil
	}
	if data == nil {
		return nil
	}
	return gin.H(data)
}

func (p *Plugin) OnInit(_ *gorm.DB) error {
	if !p.ext.FunctionExists("on_init") {
		return nil
	}
	settings := map[string]string{}
	for _, s := range p.settings {
		settings[s.Key] = s.DefaultValue
	}
	if err := p.callJSON("on_init", initInput{Settings: settings}, nil, jobTimeout); err != nil {
		return fmt.Errorf("wasm plugin %s: on_init: %w", p.name, err)
	}
	return nil
}

// RenderPage maps the render_page result onto goblog's page rendering:
// html → page_content.html; template+data → passed through; raw → written
// directly. Errors and unknown shapes decline the request (404 upstream).
func (p *Plugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	owned := false
	for _, pg := range p.pages {
		if pg.PageType == pageType {
			owned = true
		}
	}
	if !owned || !p.ext.FunctionExists("render_page") {
		return "", nil
	}
	var res renderResult
	if err := p.callJSON("render_page", makeCtx(ctx), &res, callTimeout); err != nil {
		p.opts.Logf("plugin %s: render_page %q: %v", p.name, ctx.SubPath, err)
		return "", nil
	}
	switch {
	case res.Raw != nil:
		if ctx.GinContext == nil {
			return "", nil
		}
		status := res.Raw.Status
		if status == 0 {
			status = 200
		}
		ct := res.Raw.ContentType
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		ctx.GinContext.Data(status, ct, []byte(res.Raw.Body))
		return "", nil
	case res.Template != "":
		data := gin.H{}
		for k, v := range res.Data {
			data[k] = v
		}
		return res.Template, data
	case res.HTML != nil:
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": *res.HTML}
	}
	return "", nil
}
```

Notes for the implementer: (1) `OnInit` receives default settings because the registry seeds them right before calling it and the plugin has no other way to read them; document this in the contract comment. (2) `extism.Plugin.Timeout` is set per call under the mutex — that is what `CallWithContext` uses. (3) After a timeout wazero closes the module; subsequent calls return "module is closed" errors, which the adapter logs — the registry keeps the plugin registered; a restart or reinstall recovers it. Log it once with a clear message ("plugin X: instance closed after a timeout; reinstall or restart to recover") the first time it happens.

- [ ] **Step 5: Run** `go test -race ./plugin/wasm/ -count=1 -v` → PASS. Also `go vet ./plugin/wasm/`.

If `TestHTTPAllowedHosts` fails because Extism's host matching needs the port, change the allowed host in the test to the literal `srv.Listener.Addr().String()` host part (`127.0.0.1`) — Extism matches `url.Hostname()` with glob; `"127.0.0.1"` is correct. If `TestTimeoutAndMemoryCap`'s `big` case succeeds (module grows memory beyond 1024 pages), verify `MaxPages` reached wazero (`WithMemoryLimitPages`) — see `plugin.go:130` in the SDK; Go's wasm runtime may fail the allocation with a panic → the call errors → declined, which is what the test expects.

- [ ] **Step 6: Commit**

```bash
git branch --show-current
git add go.mod go.sum plugin/wasm/
git commit -m "Run WebAssembly plugins through Extism behind the plugin interface

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Loader, sidecar, runtime label, `validate-plugin` for `.wasm`

**Files:**
- Create: `plugin/wasm/loader.go`, `plugin/wasm/loader_test.go`
- Modify: `plugin/registry.go` (`DynamicInfo.Runtime`), `plugin/validate.go` (`Info.Runtime`), `cmd_validate.go` (+test), `goblog.go`

**Interfaces:**
- Produces: `wasm.LoadWasmPlugins(registry *plugin.Registry, dir string, store plugin.Store) `; `wasm.ReadSidecar(path string) (allowedHosts []string, err error)`; `wasm.WriteSidecar(path string, allowedHosts []string) error` (sidecar path = wasm path with `.wasm` → `.json`); `wasm.SidecarPath(wasmPath string) string`; `plugin.DynamicInfo.Runtime string \`json:"runtime"\``; `plugin.Info.Runtime string \`json:"runtime,omitempty"\``; `wasm.Validate(path string) (plugin.Info, error)`.

- [ ] **Step 1: Failing tests** — `plugin/wasm/loader_test.go`:

```go
package wasm

import (
	"os"
	"path/filepath"
	"testing"

	"goblog/plugin"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadWasmPlugins(t *testing.T) {
	dir := t.TempDir()
	src := echoBytes(t)
	os.WriteFile(filepath.Join(dir, "echo.wasm"), src, 0644)
	os.WriteFile(filepath.Join(dir, "broken.wasm"), []byte("nope"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644)
	os.Mkdir(filepath.Join(dir, "sub"), 0755)
	if err := WriteSidecar(filepath.Join(dir, "echo.wasm"), []string{"api.example.test"}); err != nil {
		t.Fatal(err)
	}
	if SidecarPath(filepath.Join(dir, "echo.wasm")) != filepath.Join(dir, "echo.json") {
		t.Error("sidecar path")
	}

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	LoadWasmPlugins(reg, dir, reg.Store())
	dyn := reg.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "echo" || dyn[0].Runtime != "wasm" || dyn[0].Path != filepath.Join(dir, "echo.wasm") {
		t.Fatalf("dynamic = %+v", dyn)
	}
	p := reg.Plugins()[0].(*Plugin)
	if hosts := p.AllowedHosts(); len(hosts) != 1 || hosts[0] != "api.example.test" {
		t.Errorf("allowed hosts from sidecar = %v", hosts)
	}
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := reg.Store().Get("echo", "init"); string(v) != "1" {
		t.Error("on_init should have run through the registry with the real store")
	}
	// Missing directory is not an error.
	LoadWasmPlugins(plugin.NewRegistry(db), filepath.Join(dir, "missing"), reg.Store())
}

func TestSidecar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.wasm")
	if hosts, err := ReadSidecar(p); err != nil || hosts != nil {
		t.Errorf("no sidecar → nil, nil; got %v %v", hosts, err)
	}
	os.WriteFile(SidecarPath(p), []byte(`{"allowed_hosts": ["a", "b"]}`), 0644)
	if hosts, err := ReadSidecar(p); err != nil || len(hosts) != 2 {
		t.Errorf("sidecar = %v %v", hosts, err)
	}
	os.WriteFile(SidecarPath(p), []byte(`{bad`), 0644)
	if _, err := ReadSidecar(p); err == nil {
		t.Error("malformed sidecar should error")
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.wasm")
	os.WriteFile(path, echoBytes(t), 0644)
	info, err := Validate(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "echo" || info.DisplayName != "Echo" || info.Version != "1.2.3" || info.Runtime != "wasm" {
		t.Errorf("info = %+v", info)
	}
	bad := filepath.Join(dir, "bad.wasm")
	os.WriteFile(bad, []byte("nope"), 0644)
	if _, err := Validate(bad); err == nil {
		t.Error("garbage should fail validation")
	}
}
```

Add to `plugin/registry_test.go`'s `TestUnregisterStopsOnlyThatPluginsJobs` (after `dyn := reg.Dynamic()` checks): `if dyn[0].Runtime != "go" { t.Errorf("runtime = %q", dyn[0].Runtime) }`.

`cmd_validate_test.go` — add to `TestRunValidatePlugin`:
```go
	// .wasm files are validated through the wasm runtime.
	out.Reset()
	errOut.Reset()
	if code := runValidatePlugin([]string{"plugin/wasm/testdata/echo.wasm"}, &out, &errOut); code != 0 {
		t.Fatalf("wasm: exit %d, stderr %q", code, errOut.String())
	}
	json.Unmarshal(out.Bytes(), &info)
	if info["name"] != "echo" || info["version"] != "1.2.3" || info["runtime"] != "wasm" {
		t.Errorf("wasm identity: %v", info)
	}
```
(`info` there is `map[string]string`; `runtime` is a string, fine.)

- [ ] **Step 2: Run** `go test ./plugin/wasm/ -run 'TestLoadWasm|TestSidecar|TestValidate' -v; go test . -run TestRunValidatePlugin` → FAIL to compile.

- [ ] **Step 3: Implement**

`plugin/registry.go`: add `Runtime string \`json:"runtime"\`` to `DynamicInfo`; in `Dynamic()` set `Runtime: runtimeOf(e.path)` with
```go
func runtimeOf(path string) string {
	if strings.HasSuffix(path, ".wasm") {
		return "wasm"
	}
	return "go"
}
```
(import `strings`). `plugin/validate.go`: add `Runtime string \`json:"runtime,omitempty"\`` to `Info`.

`plugin/wasm/loader.go`:
```go
package wasm

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"goblog/plugin"
)

// sidecar is plugins/wasm/<name>.json next to <name>.wasm.
type sidecar struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

// SidecarPath returns the sidecar file for a .wasm path.
func SidecarPath(wasmPath string) string {
	return strings.TrimSuffix(wasmPath, filepath.Ext(wasmPath)) + ".json"
}

// ReadSidecar returns the allowed hosts declared next to a plugin file;
// nil when there is no sidecar.
func ReadSidecar(wasmPath string) ([]string, error) {
	b, err := os.ReadFile(SidecarPath(wasmPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s sidecar
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", SidecarPath(wasmPath), err)
	}
	return s.AllowedHosts, nil
}

// WriteSidecar records the allowed hosts for a plugin file.
func WriteSidecar(wasmPath string, allowedHosts []string) error {
	if allowedHosts == nil {
		allowedHosts = []string{}
	}
	b, _ := json.MarshalIndent(sidecar{AllowedHosts: allowedHosts}, "", "  ")
	return os.WriteFile(SidecarPath(wasmPath), append(b, '\n'), 0644)
}

// LoadWasmPlugins loads every *.wasm in dir and registers it. A file that
// fails to load is logged and skipped; a missing dir is not an error.
func LoadWasmPlugins(registry *plugin.Registry, dir string, store plugin.Store) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read wasm plugin directory %s: %v", dir, err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wasm") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		hosts, err := ReadSidecar(path)
		if err != nil {
			log.Printf("Warning: skipping %s: %v", e.Name(), err)
			continue
		}
		p, err := Load(path, Options{Store: store, AllowedHosts: hosts})
		if err != nil {
			log.Printf("Warning: failed to load wasm plugin %s: %v", e.Name(), err)
			continue
		}
		if err := registry.RegisterDynamic(p, path); err != nil {
			log.Printf("Warning: skipping wasm plugin %s: %v", e.Name(), err)
			p.Close()
		}
	}
}

// Validate loads a .wasm file with no store or network and reports its
// identity — what `goblog validate-plugin` prints for wasm files.
func Validate(path string) (plugin.Info, error) {
	p, err := Load(path, Options{})
	if err != nil {
		return plugin.Info{}, err
	}
	defer p.Close()
	id := p.Identity()
	return plugin.Info{Name: id.Name, DisplayName: id.DisplayName, Version: id.Version, Runtime: "wasm"}, nil
}
```

`cmd_validate.go`: branch on extension:
```go
	var info gplugin.Info
	var err error
	if strings.HasSuffix(strings.ToLower(args[0]), ".wasm") {
		info, err = wasm.Validate(args[0])
	} else {
		info, err = gplugin.Validate(args[0])
	}
```
(imports `strings`, `"goblog/plugin/wasm"`).

`goblog.go`: after the Yaegi block:
```go
	if os.Getenv("ENABLE_WASM_PLUGINS") != "false" {
		wasm.LoadWasmPlugins(registry, "plugins/wasm", registry.Store())
	}
```
(import `"goblog/plugin/wasm"`). Also add `plugins/wasm/` handling to `.gitignore` the same way `plugins/dynamic/*.go` is handled — check `.gitignore`; add `plugins/wasm/*` with `!plugins/wasm/.gitkeep` and create the `.gitkeep`.

- [ ] **Step 4: Run** `go build ./... && go test -race ./plugin/... . -count=1` → PASS.
- [ ] **Step 5: Commit** — `git add plugin/ cmd_validate.go cmd_validate_test.go goblog.go .gitignore plugins/wasm/.gitkeep && git commit -m "Load wasm plugins from plugins/wasm and validate .wasm files

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"`

---

### Task 4: Installer and directory types for wasm

**Files:**
- Modify: `plugins/directory/index.go` (`Runtime`, `AllowedHosts`), `plugin/installer/installer.go`, `plugin/installer/installer_test.go`, `goblog.go` (installer `WasmDir`)

**Interfaces:**
- Consumes: `wasm.LoadBytes`, `wasm.WriteSidecar`, `wasm.SidecarPath`, `plugin.Store` via `Registry.Store()`.
- Produces: `Installer.WasmDir string`; `Installed.Runtime string \`json:"runtime"\`` (`"wasm"`, `"go"`, `"builtin"`); `Available` now carries `Runtime`/`AllowedHosts` through the embedded `Entry`; `maxWasmBytes = 16 << 20`.

- [ ] **Step 1: Failing tests** — in `plugin/installer/installer_test.go`:

Extend the fixture: add `echoWasm []byte` loaded from `../../plugin/wasm/testdata/echo.wasm` in `newFixture`, serve it at `/echo.wasm`, and a helper `f.wasmEntry(name, version string, hosts []string)` producing an index entry with `"install_type": "wasm"`, `"runtime": "wasm"`, `"allowed_hosts": hosts`, `"download_url": f.srv.URL + "/echo.wasm"`, `"sha256": sum(f.echoWasm)`. Set `inst.WasmDir = t.TempDir()` in `newInstaller`. Then:

```go
func TestInstallWasm_HappyPathAndUninstall(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", []string{"api.example.test"})}
	inst := newInstaller(t, f)
	ctx := context.Background()

	res, err := inst.Install(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("result = %+v", res)
	}
	path := filepath.Join(inst.WasmDir, "echo.wasm")
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, f.echoWasm) {
		t.Fatalf("wasm file not written verbatim: %v", err)
	}
	if hosts, _ := wasm.ReadSidecar(path); len(hosts) != 1 || hosts[0] != "api.example.test" {
		t.Errorf("sidecar hosts = %v", hosts)
	}
	dyn := inst.Registry.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "echo" || dyn[0].Runtime != "wasm" || dyn[0].Path != path {
		t.Errorf("dynamic = %+v", dyn)
	}
	st := inst.Status()
	if len(st.Installed) != 1 || st.Installed[0].Runtime != "wasm" {
		t.Errorf("installed = %+v", st.Installed)
	}
	// on_init ran with the real store
	if v, _, _ := inst.Registry.Store().Get("echo", "init"); string(v) != "1" {
		t.Error("on_init should have written to the store")
	}

	if err := inst.Uninstall("echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("wasm file should be deleted")
	}
	if _, err := os.Stat(wasm.SidecarPath(path)); err == nil {
		t.Error("sidecar should be deleted")
	}
	if keys, _ := inst.Registry.Store().List("echo", ""); len(keys) != 0 {
		t.Error("store rows should be deleted on uninstall")
	}
}

func TestInstall_RefusesNonWasmTypes(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f) // default index: hello (dynamic/.go), scholar (compiled-in), future
	if _, err := inst.Install(context.Background(), "hello"); !errors.Is(err, ErrNotDynamic) {
		t.Errorf(".go directory entries are no longer installable: %v", err)
	}
	st := inst.Status()
	for _, a := range st.Available {
		if a.Compatible {
			t.Errorf("%s should be marked incompatible (not wasm): %+v", a.Name, a)
		}
	}
}

func TestInstallWasm_IdentityMismatchAndUpdate(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "9.9.9", nil)} // version disagrees with the module
	inst := newInstaller(t, f)
	if _, err := inst.Install(context.Background(), "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("version mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "echo.wasm")); err == nil {
		t.Error("nothing may be written on identity mismatch")
	}
	// Install the right version, then "update" to a newer index version served by the same bytes → ErrLoad + rollback.
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst.Refresh()
	if _, err := inst.Install(context.Background(), "echo"); err != nil {
		t.Fatal(err)
	}
	f.entries = []map[string]any{f.wasmEntry("echo", "2.0.0", nil)}
	inst.Refresh()
	if _, err := inst.Update(context.Background(), "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("update with mismatching module: %v", err)
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 || dyn[0].Version != "1.2.3" {
		t.Errorf("previous plugin must remain: %+v", dyn)
	}
}
```

Existing tests that installed `hello` (a `.go` entry) must switch to wasm fixtures: change `newFixture`'s default index so `hello` becomes a wasm entry named `echo`… simplest: keep the old fixture entries for `TestInstall_RefusesNonWasmTypes` and give every other install test `f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}` and use `"echo"` instead of `"hello"`. Update assertions accordingly (`Installed Echo v1.2.3`, settings key `greeting` instead of `message`, file `echo.wasm` in `WasmDir`). Rollback tests use `hookBeforeRegister` unchanged. Concurrent-install test: same with echo.

- [ ] **Step 2: Run** `go test ./plugin/installer/ -count=1` → FAIL to compile (`inst.WasmDir`, `wasm` import, `Runtime`).

- [ ] **Step 3: Implement**

`plugins/directory/index.go` `Entry` — add after `Stars`:
```go
	Runtime          string   `json:"runtime"`       // "wasm"; the only installable runtime
	AllowedHosts     []string `json:"allowed_hosts"` // hosts the plugin may call
```

`plugin/installer/installer.go`:
- `Installer` gains `WasmDir string`.
- `ErrNotDynamic` text: `"this plugin type cannot be installed from the directory"`.
- `lookup`: `if e.InstallType != "wasm" { return ErrNotDynamic }` (replace the `"dynamic"` check).
- `Status`: reason for non-wasm becomes `"not installable from the directory"`; `Installed.Runtime`: `"builtin"` for non-dynamic rows, else `d.Runtime`.
- `fetchAndCheck` → returns `(src []byte, p plugin.Plugin, err)`; for wasm: `p, err := wasm.LoadBytes(src, wasm.Options{Store: i.Registry.Store(), AllowedHosts: e.AllowedHosts})`; identity check via `p.Name()/Version()` as before; download cap `maxWasmBytes = 16 << 20` when `e.InstallType == "wasm"`.
- `Install`: `path := filepath.Join(i.WasmDir, e.Name+".wasm")`; write sidecar first (`wasm.WriteSidecar(path, e.AllowedHosts)`), then `writeAtomic(path, src)`, then `register`. On failure remove both files. If `register` fails, also `p.(*wasm.Plugin).Close()`.
- `Update`: same swap dance on `d.Path` (`.prev` for the wasm; sidecar rewritten from the new entry, previous sidecar kept as `.json.prev` and restored on rollback); on success close the old instance (`old.(*wasm.Plugin).Close()` if it is one).
- `Uninstall`: after `Unregister`, close the instance if wasm, remove the file and `wasm.SidecarPath(d.Path)`, `DeleteSettings`, `Registry.Store().DeleteAll(name)`.
- Keep the Yaegi path for `Dir` only where `Uninstall`/`Update` operate on an installed `.go` plugin (`d.Runtime == "go"` → no sidecar, no Close).

`goblog.go`: `WasmDir: "plugins/wasm"` on the installer.

- [ ] **Step 4: Run** `go build ./... && go test -race ./plugin/installer/ ./plugins/directory/ -count=1` → PASS.
- [ ] **Step 5: Commit** — `git add plugins/directory/index.go plugin/installer/ goblog.go && git commit -m "Install WebAssembly plugins from the directory

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"`

---

### Task 5: Admin page, README, verification

**Files:**
- Modify: `admin/plugins.go` (nothing functional; confirm JSON passes `runtime`/`allowed_hosts`), `themes/default/templates/admin_plugins.html`, `admin/plugins_test.go`, `README.md`, `plugins/directory/templates/listing.html` + `detail.html` (runtime + hosts), `plugins/directory/directory_test.go`

- [ ] **Step 1: Failing tests**

`admin/plugins_test.go` `TestAdminPluginsPage`: add markers `"WebAssembly"` and `"Talks to"` to the required-strings list (they appear in the page copy / JS templates). `plugins/directory/directory_test.go`: in the listing test fixture add `"runtime":"wasm","allowed_hosts":["api.example.test"]` to one entry and assert the listing contains `wasm` and `api.example.test`; the detail test asserts `"Talks to"` appears when hosts are set and `no network` when not.

- [ ] **Step 2: Implement**

`admin_plugins.html`:
- Intro copy: "Directory plugins run sandboxed in WebAssembly and can add pages, background jobs, settings and head/footer HTML. They can only reach the hosts they declare."
- Installed rows: badge from `p.runtime` — `wasm` → "wasm", `go` → "go (local)", else "built-in".
- Browse cards: line `Talks to: <hosts joined by ", ">` or `No network access`, escaped; `p.runtime !== "wasm"` shows the server's reason.
- Disabled-banner text now refers to `plugins/wasm/` being writable (WASM is on by default; the Yaegi banner text stays for the `.go` case only if `dynamic_enabled` is false **and** dir not writable — simplify: banner shows `dir_error` when set, otherwise nothing).

`Installer.Status`: `DynamicEnabled` stays (Yaegi); add `WasmDirWritable bool`/`WasmDirError string` probed on `WasmDir`, and the Install buttons gate on `wasm_dir_writable`. Update `TestStatus_DirWritability` to cover `WasmDir`.

`listing.html`/`detail.html`: a "Runtime" column/row (`wasm`) and "Talks to" row.

`README.md`:
- Plugins feature bullets: "WebAssembly plugins (sandboxed, any language/dependencies) installable from the directory".
- New "### WebAssembly plugins" section under "## Plugins": the host contract table (exports + host functions), the `ctx` shape, limits, how to build one (`GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .` with `github.com/extism/go-pdk`), where files live (`plugins/wasm/<name>.wasm` + `<name>.json` sidecar with `allowed_hosts`), `ENABLE_WASM_PLUGINS=false` to disable, `goblog validate-plugin plugin.wasm`. Point to `plugin/wasm/testdata/echo/main.go` as the reference implementation of every export.
- "### Dynamic plugins" (Yaegi): note they are the local/operator path and not a directory format.
- Docker: add the `plugins/wasm` bind mount to the example.

- [ ] **Step 3: Full verification**

`go build -v . && go vet ./... && go test -race goblog/... -count=1` → PASS. Manual: start goblog (`go run .`, fresh sqlite, `.env` with `database=sqlite`), copy `plugin/wasm/testdata/echo.wasm` to `plugins/wasm/echo.wasm`, restart → log shows `Plugin registered: Echo v1.2.3`; `/echo` renders "echo" + greeting; `/echo/data.json` → JSON; footer shows `<p>hi</p>`; Admin → Plugins lists Echo as `wasm`; `./goblog validate-plugin plugins/wasm/echo.wasm` prints the identity with `"runtime":"wasm"`. Record the transcript in the report.

- [ ] **Step 4: Commit and PR**

```bash
git branch --show-current
git add -A
git commit -m "Show WebAssembly plugins in the admin and directory pages; document the host contract

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/wasm-plugins
gh pr create --base main --title "WebAssembly plugin runtime (Extism)" --body "$(cat <<'EOF'
## Summary
Sandboxed WebAssembly plugins, per `docs/superpowers/specs/2026-09-18-wasm-plugins-design.md` §1–2:
- `plugin/wasm`: Extism-backed adapter implementing `plugin.Plugin` — exports `identity`, `settings`, `pages`, `jobs`, `template_head/footer/data`, `render_page`, `run_job`, `on_init`; host functions `store_get/set/delete/list` on a new per-plugin `plugin_store` table; HTTP limited to declared `allowed_hosts`; 10 s / 120 s call timeouts, 64 MB memory cap, one mutex-guarded instance per plugin.
- Loader for `plugins/wasm/*.wasm` (+ `<name>.json` sidecar), on by default (`ENABLE_WASM_PLUGINS=false` to disable).
- `goblog validate-plugin` accepts `.wasm`.
- Installer/admin: `install_type: wasm` is now the only directory-installable type; runtime badges and "Talks to" hosts in the UI and the public directory.
- Test fixture `plugin/wasm/testdata/echo.wasm` (source in `testdata/echo/`, rebuilt with `go generate ./plugin/wasm`).

Registry contract v2 (`runtime: wasm`, release-asset `.wasm`, `allowed_hosts`) and the first wasm plugins (hello v2, scholar) follow in `goblogplatform/plugins` and their own repos.

## Test plan
- [x] `go build -v . && go vet ./... && go test -race goblog/...`
- [x] Manual: echo fixture in `plugins/wasm/`, page/footer/json render, admin badge, validate-plugin

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Do **not** merge.

---

## Self-review notes

- Spec §1 coverage: every export (Task 2 adapter + fixture), `ctx` shape (`makeCtx`), host functions (Task 2 `host.go` + Task 1 store), HTTP via Extism `AllowedHosts` (manifest in `LoadBytes`), limits (`callTimeout`/`jobTimeout`/`memoryPages`/`maxHTTPResponseBytes`), single instance + mutex (`call`). §2: adapter/caching (Task 2), sidecar + loader + env flag (Task 3), `validate-plugin` (Task 3), installer (Task 4), admin/directory UI + docs (Task 5), fixture + tests (Tasks 2–4).
- Deviations from the spec text, deliberate: host functions live in Extism's default `extism:host/user` namespace (matches PDK docs) rather than a `goblog` namespace; logging uses Extism's built-in `pdk.Log` rather than a custom `log` host function; `run_job` and `on_init` receive `settings`. All three are recorded in the README contract table. A fourth, from the whole-branch review: a `render_page` export error (non-zero exit, runtime trap, timeout, invalid JSON) declines the request — the page 404s — rather than rendering the scholar-style "plugin unavailable" notice.
- Type consistency: `Options{Store, AllowedHosts, Logf}`, `LoadBytes`, `Load`, `Validate`, `SidecarPath/ReadSidecar/WriteSidecar` (Tasks 2–3) match their uses in Task 4; `DynamicInfo.Runtime` (Task 3) used by Task 4's `Installed.Runtime`; `Entry.Runtime/AllowedHosts` (Task 4) used by Task 5 templates.
