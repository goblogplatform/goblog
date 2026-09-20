package installer

import (
	"bytes"
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
	"sync"
	"sync/atomic"
	"testing"

	"goblog/blog"
	"goblog/plugin"
	"goblog/plugin/wasm"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fixture serves the echo wasm test plugin (v1.2.3, plus a copy whose
// embedded version string is patched to 1.2.4 so updates have somewhere to
// go), the Yaegi hello example (only installable-type refusals use it), and
// an index built from f.entries.
type fixture struct {
	srv        *httptest.Server
	helloV1    []byte
	echoWasm   []byte
	echoWasmV2 []byte
	entries    []map[string]any
	checksumOK atomic.Bool // when false, wasmEntry carries a wrong sha256 for echo
	hits       atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	src, err := os.ReadFile("../../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatal(err)
	}
	echo, err := os.ReadFile("../../plugin/wasm/testdata/echo.wasm")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{helloV1: src, echoWasm: echo}
	f.checksumOK.Store(true)
	// The version literal is a same-length rodata string in the module, so
	// patching it in place yields a valid module that reports 1.2.4.
	if bytes.Count(echo, []byte("1.2.3")) != 1 {
		t.Fatal("fixture: expected exactly one \"1.2.3\" in echo.wasm; rebuild the fixture or adjust the patch")
	}
	f.echoWasmV2 = bytes.Replace(echo, []byte("1.2.3"), []byte("1.2.4"), 1)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(f.index())
		case "/echo.wasm":
			w.Write(f.echoWasm)
		case "/echo-v2.wasm":
			w.Write(f.echoWasmV2)
		case "/hello.go":
			w.Write(f.helloV1)
		case "/redirect":
			http.Redirect(w, r, "http://example.invalid/x.wasm", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// capitalize upper-cases the first byte; good enough for the plugin names
// used in these fixtures.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// entry is a legacy Yaegi (.go) directory entry, kept only to prove that
// such entries are no longer installable.
func (f *fixture) entry(name, version, minVersion string, stars int) map[string]any {
	return map[string]any{
		"name": name, "display_name": capitalize(name), "description": "d", "version": version,
		"author": "a", "license": "MIT", "source_url": "https://github.com/x/" + name,
		"download_url": f.srv.URL + "/hello.go", "sha256": sum(f.helloV1), "min_goblog_version": minVersion,
		"install_type": "dynamic", "released_at": "2026-09-15T00:00:00Z",
		"detail_url": f.srv.URL + "/plugins/" + name + ".json", "stars": stars,
	}
}

// wasmEntry is a directory entry served by the echo module (v1.2.3).
func (f *fixture) wasmEntry(name, version string, hosts []string) map[string]any {
	sha := sum(f.echoWasm)
	if name == "echo" && !f.checksumOK.Load() {
		sha = strings.Repeat("0", 64)
	}
	return map[string]any{
		"name": name, "display_name": capitalize(name), "description": "d", "version": version,
		"author": "a", "license": "MIT", "source_url": "https://github.com/x/" + name,
		"download_url": f.srv.URL + "/echo.wasm", "sha256": sha, "min_goblog_version": "0.2.6",
		"install_type": "wasm", "runtime": "wasm", "allowed_hosts": hosts,
		"released_at": "2026-09-15T00:00:00Z",
		"detail_url":  f.srv.URL + "/plugins/" + name + ".json", "stars": 0,
	}
}

// wasmEntryV2 is the echo entry at 1.2.4, served by the patched module.
func (f *fixture) wasmEntryV2(hosts []string) map[string]any {
	e := f.wasmEntry("echo", "1.2.4", hosts)
	e["download_url"] = f.srv.URL + "/echo-v2.wasm"
	e["sha256"] = sum(f.echoWasmV2)
	return e
}

// index is the default directory: the legacy hello (.go) entry, a
// compiled-in entry and a too-new one. Tests set f.entries to change it;
// most use wasmEntries().
func (f *fixture) index() []map[string]any {
	if f.entries != nil {
		return f.entries
	}
	compiled := f.entry("scholar", "1.0.0", "0.1.0", 50)
	compiled["install_type"] = "compiled-in"
	return []map[string]any{
		f.entry("hello", "1.0.0", "0.2.6", 3),
		compiled,
		f.entry("future", "1.0.0", "9.0.0", 1),
	}
}

// wasmEntries is the wasm-era default: echo 1.2.3 (3 stars), a compiled-in
// scholar (50) and a wasm entry that needs a newer goblog (1).
func (f *fixture) wasmEntries() []map[string]any {
	echo := f.wasmEntry("echo", "1.2.3", nil)
	echo["stars"] = 3
	compiled := f.entry("scholar", "1.0.0", "0.1.0", 50)
	compiled["install_type"] = "compiled-in"
	future := f.wasmEntry("future", "1.0.0", nil)
	future["min_goblog_version"], future["stars"] = "9.0.0", 1
	return []map[string]any{echo, compiled, future}
}

func newInstaller(t *testing.T, f *fixture) *Installer {
	t.Helper()
	inst, _ := newInstallerDB(t, f)
	return inst
}

// newInstallerDB also returns the registry's database for direct queries.
func newInstallerDB(t *testing.T, f *fixture) (*Installer, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	return &Installer{
		Dir:         t.TempDir(),
		WasmDir:     t.TempDir(),
		Registry:    reg,
		Directory:   NewFetcher(f.srv.Client()),
		Version:     "v0.2.7",
		Client:      f.srv.Client(),
		Enabled:     true,
		WasmEnabled: true,
		IndexURL:    func() string { return f.srv.URL + "/index.json" },
	}, db
}

// setting returns the current value of one plugin setting, "" if unset.
func setting(reg *plugin.Registry, name, key string) string {
	for _, g := range reg.GetAllSettings() {
		if g.PluginName == name {
			return g.CurrentValues[key]
		}
	}
	return ""
}

// footer calls the registered plugin's TemplateFooter, proving the instance
// behind the registry is alive (a closed wasm instance answers "").
func footer(reg *plugin.Registry, name string) string {
	for _, p := range reg.Plugins() {
		if p.Name() == name {
			return p.TemplateFooter(&plugin.HookContext{Settings: map[string]string{"enabled": "true", "greeting": "alive"}})
		}
	}
	return ""
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	f.entries = f.wasmEntries()
	inst := newInstaller(t, f)
	inst.Registry.Register(&compiledPlugin{})

	st := inst.Status()
	if !st.DynamicEnabled || !st.WasmEnabled || st.DirectoryURL != f.srv.URL+"/index.json" || st.IndexError != "" || st.IndexFetchedAt == "" {
		t.Errorf("status = %+v", st)
	}
	if !st.DirWritable || st.DirError != "" {
		t.Errorf("a fresh WasmDir should be writable: %+v", st)
	}
	if len(st.Installed) != 1 || st.Installed[0].Name != "scholar" || st.Installed[0].Dynamic || st.Installed[0].UpdateAvailable || st.Installed[0].Runtime != "builtin" {
		t.Errorf("installed = %+v", st.Installed)
	}
	// scholar is installed (compiled-in) so it is not "available"; echo and future are.
	if len(st.Available) != 2 || st.Available[0].Name != "echo" || st.Available[1].Name != "future" {
		t.Fatalf("available = %+v", st.Available)
	}
	if !st.Available[0].Compatible || st.Available[1].Compatible || !strings.Contains(st.Available[1].Reason, "9.0.0") {
		t.Errorf("compatibility: %+v", st.Available)
	}
	if st.Available[0].Stars != 3 || st.Available[0].Runtime != "wasm" {
		t.Errorf("stars and runtime should come through, got %+v", st.Available[0].Entry)
	}
	if n := f.hits.Load(); n != 1 {
		t.Errorf("Status should make exactly one index request, got %d", n)
	}
}

// TestStatus_SkipsInvalidNamesAndSanitizesUnsafeURLs checks that Status()
// applies the same name-safety rule as install-time lookup() to directory
// entries (an entry with an invalid name must not reach the Available list
// or any map keyed by it) and strips any source_url/download_url that is
// not an absolute http(s) URL, since both render into admin-page attributes
// (an href, a JS string literal) that untrusted directory data must not be
// able to break out of.
func TestStatus_SkipsInvalidNamesAndSanitizesUnsafeURLs(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)

	bad := f.wasmEntry("bad name", "1.0.0", nil)
	unsafe := f.wasmEntry("unsafe", "1.0.0", nil)
	unsafe["source_url"] = "javascript:alert(1)"
	f.entries = []map[string]any{bad, unsafe}

	st := inst.Status()
	for _, a := range st.Available {
		if a.Name == "bad name" {
			t.Fatalf("expected the invalid-name entry to be excluded: %+v", st.Available)
		}
	}
	if len(st.Available) != 1 || st.Available[0].Name != "unsafe" {
		t.Fatalf("available = %+v", st.Available)
	}
	if st.Available[0].SourceURL != "" {
		t.Errorf("expected an unsafe source_url to be sanitized to empty, got %q", st.Available[0].SourceURL)
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
	if _, err := inst.Install(context.Background(), "echo"); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Errorf("install without an index should be ErrDirectoryUnavailable, got %v", err)
	}
}

func TestInstallWasm_HappyPathAndUninstall(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", []string{"api.example.test"})}
	inst, db := newInstallerDB(t, f)
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
	// The registry created the page the plugin declares.
	var count int64
	db.Model(&blog.Page{}).Where("page_type = ?", "echo").Count(&count)
	if count != 1 {
		t.Fatalf("expected the echo page row after install, got %d", count)
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
	db.Model(&blog.Page{}).Where("page_type = ?", "echo").Count(&count)
	if count != 0 {
		t.Errorf("the plugin's page row should be deleted on uninstall, got %d", count)
	}
}

// TestInstall_RefusesExistingFile: a module already sitting at
// plugins/wasm/<name>.wasm that no registered plugin owns (dropped by hand,
// or one that failed to load at boot) is not silently overwritten.
func TestInstall_RefusesExistingFile(t *testing.T) {
	f := newFixture(t)
	f.entries = f.wasmEntries()
	inst := newInstaller(t, f)
	path := filepath.Join(inst.WasmDir, "echo.wasm")
	if err := os.WriteFile(path, []byte("stray"), 0644); err != nil {
		t.Fatal(err)
	}
	inst.Status() // caches the index so the only request Install could make is the download
	hits := f.hits.Load()
	_, err := inst.Install(context.Background(), "echo")
	if !errors.Is(err, ErrAlreadyInstalled) || !strings.Contains(err.Error(), "remove it first") {
		t.Fatalf("expected an already-installed error naming the file, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "stray" {
		t.Error("the existing file must be left untouched")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing may be registered")
	}
	if f.hits.Load() != hits {
		t.Error("nothing should be downloaded when the file already exists")
	}
	// Once the stray file is gone the install goes through.
	os.Remove(path)
	if _, err := inst.Install(context.Background(), "echo"); err != nil {
		t.Fatalf("install after removing the file: %v", err)
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

func TestInstall_HappyPath(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst := newInstaller(t, f)

	res, err := inst.Install(context.Background(), "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "echo" || res.Version != "1.2.3" || res.Message != "Installed Echo v1.2.3" {
		t.Errorf("result = %+v", res)
	}
	// Settings seeded (InitPlugin ran).
	if setting(inst.Registry, "echo", "greeting") == "" {
		t.Error("echo's settings should be seeded after install")
	}
	if got := footer(inst.Registry, "echo"); got != "<p>alive</p>" {
		t.Errorf("the registered instance should answer hooks, got %q", got)
	}
	st := inst.Status()
	if len(st.Installed) != 1 || !st.Installed[0].Dynamic || st.Installed[0].UpdateAvailable {
		t.Errorf("installed after install = %+v", st.Installed)
	}
	if _, err := inst.Install(context.Background(), "echo"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("second install should be ErrAlreadyInstalled, got %v", err)
	}
}

func TestInstall_Refusals(t *testing.T) {
	f := newFixture(t)
	f.entries = f.wasmEntries()
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
	f.entries = f.wasmEntries()
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(ctx, "echo"); !errors.Is(err, ErrChecksum) {
		t.Errorf("bad checksum: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "echo.wasm")); err == nil {
		t.Error("nothing may be written on a checksum mismatch")
	}
	f.checksumOK.Store(true)

	// Name() disagrees with the index entry.
	f.entries = []map[string]any{f.wasmEntry("hello", "1.2.3", nil)}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(ctx, "hello"); !errors.Is(err, ErrLoad) {
		t.Errorf("name mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "hello.wasm")); err == nil {
		t.Error("nothing may be written when the plugin's identity does not match")
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "hello.json")); err == nil {
		t.Error("no sidecar may be written when the plugin's identity does not match")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing may be registered on a failed install")
	}

	// Installs are gated on the wasm runtime, not on the Yaegi flag.
	inst.Enabled = false
	f.entries = f.wasmEntries()
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(ctx, "echo"); err != nil {
		t.Errorf("install with only ENABLE_DYNAMIC_PLUGINS off should work: %v", err)
	}
	inst.WasmEnabled = false
	if _, err := inst.Install(ctx, "future"); !errors.Is(err, ErrWasmDisabled) {
		t.Errorf("install while disabled: %v", err)
	}
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrWasmDisabled) {
		t.Errorf("update while disabled: %v", err)
	}
	if err := inst.Uninstall("echo"); !errors.Is(err, ErrWasmDisabled) {
		t.Errorf("uninstall while disabled: %v", err)
	}
	if st := inst.Status(); st.DynamicEnabled || st.WasmEnabled || st.DirWritable || len(st.Available) == 0 {
		t.Errorf("Status should still list the directory when wasm plugins are disabled: %+v", st)
	}
}

func TestUpdateAndRollback(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", []string{"a.example.test"})}
	inst := newInstaller(t, f)
	ctx := context.Background()
	if _, err := inst.Install(ctx, "echo"); err != nil {
		t.Fatal(err)
	}
	inst.Registry.UpdateSetting("echo", "greeting", "keep me")
	path := filepath.Join(inst.WasmDir, "echo.wasm")

	// Index now offers 1.2.4 with a different allowed host.
	f.entries = []map[string]any{f.wasmEntryV2([]string{"b.example.test"})}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if st := inst.Status(); !st.Installed[0].UpdateAvailable || st.Installed[0].LatestVersion != "1.2.4" {
		t.Errorf("update should be available: %+v", st.Installed)
	}
	res, err := inst.Update(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.2.4" || res.Message != "Updated Echo to v1.2.4" || inst.Registry.Dynamic()[0].Version != "1.2.4" {
		t.Errorf("update result = %+v, registry = %+v", res, inst.Registry.Dynamic())
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, f.echoWasmV2) {
		t.Error("echo.wasm should hold the 1.2.4 bytes after the update")
	}
	if hosts, _ := wasm.ReadSidecar(path); len(hosts) != 1 || hosts[0] != "b.example.test" {
		t.Errorf("sidecar should be rewritten from the new entry, got %v", hosts)
	}
	for _, leftover := range []string{path + ".prev", wasm.SidecarPath(path) + ".prev"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s should be removed after a successful update", filepath.Base(leftover))
		}
	}
	if setting(inst.Registry, "echo", "greeting") != "keep me" {
		t.Error("settings must survive an update")
	}
	if got := footer(inst.Registry, "echo"); got != "<p>alive</p>" {
		t.Errorf("the new instance should answer hooks, got %q", got)
	}
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrUpToDate) {
		t.Errorf("updating when already at the index version should be ErrUpToDate, got %v", err)
	}

	// The index advertises 1.3.0 but serves a module whose Version() is 1.2.4.
	// This is rejected in fetchAndCheck before anything is touched on disk or in
	// the registry, so it does not exercise the rename→unregister→rollback path;
	// TestUpdate_RollbackOnRegisterFailure below does, via hookBeforeRegister.
	bad := f.wasmEntryV2(nil)
	bad["version"] = "1.3.0"
	f.entries = []map[string]any{bad}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("version mismatch should be ErrLoad, got %v", err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, f.echoWasmV2) {
		t.Error("the previous file must be restored after a failed update")
	}
	if hosts, _ := wasm.ReadSidecar(path); len(hosts) != 1 || hosts[0] != "b.example.test" {
		t.Errorf("the previous sidecar must remain after a failed update, got %v", hosts)
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 || dyn[0].Version != "1.2.4" {
		t.Errorf("the previous plugin must be registered again after a failed update, got %+v", dyn)
	}

	if _, err := inst.Update(ctx, "scholar"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("updating a non-dynamic plugin: %v", err)
	}
}

func TestUninstall(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst := newInstaller(t, f)
	if _, err := inst.Install(context.Background(), "echo"); err != nil {
		t.Fatal(err)
	}
	if err := inst.Uninstall("echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "echo.wasm")); err == nil {
		t.Error("file should be deleted")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("plugin should be unregistered")
	}
	for _, g := range inst.Registry.GetAllSettings() {
		if g.PluginName == "echo" {
			t.Error("settings should be deleted on uninstall")
		}
	}
	if err := inst.Uninstall("echo"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("second uninstall: %v", err)
	}
	inst.Registry.Register(&compiledPlugin{})
	if err := inst.Uninstall("scholar"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("uninstalling compiled-in: %v", err)
	}
}

// TestUninstall_GoPlugin covers a Yaegi plugin that was installed before the
// directory went wasm-only: it lives in Dir, has no sidecar and nothing to
// close, and must still be removable.
func TestUninstall_GoPlugin(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	path := filepath.Join(inst.Dir, "hello.go")
	if err := os.WriteFile(path, f.helloV1, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := plugin.LoadDynamicPluginBytes(f.helloV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Registry.RegisterDynamic(p, path); err != nil {
		t.Fatal(err)
	}
	if st := inst.Status(); len(st.Installed) != 1 || st.Installed[0].Runtime != "go" {
		t.Errorf("installed = %+v", st.Installed)
	}
	if err := inst.Uninstall("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("hello.go should be deleted")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("plugin should be unregistered")
	}
}

// TestUpdate_GoPluginToWasm covers the migration path: a Yaegi plugin
// installed under Dir whose directory entry is now a wasm module. The
// module lands in WasmDir; the .go file is removed only once the new
// plugin is registered, and stays (re-registered) if that fails.
func TestUpdate_GoPluginToWasm(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	ctx := context.Background()
	goEcho := bytes.Replace(f.helloV1, []byte(`return "hello"`), []byte(`return "echo"`), 1)
	goPath := filepath.Join(inst.Dir, "echo.go")
	if err := os.WriteFile(goPath, goEcho, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := plugin.LoadDynamicPluginBytes(goEcho)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Registry.RegisterDynamic(p, goPath); err != nil {
		t.Fatal(err)
	}
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	wasmPath := filepath.Join(inst.WasmDir, "echo.wasm")

	inst.hookBeforeRegister = func(plugin.Plugin) error { return errors.New("simulated init failure") }
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}
	if _, err := os.Stat(goPath); err != nil {
		t.Error("the .go file must survive a failed update")
	}
	if _, err := os.Stat(wasmPath); err == nil {
		t.Error("the wasm module must be removed after a failed update")
	}
	if _, err := os.Stat(wasm.SidecarPath(wasmPath)); err == nil {
		t.Error("the sidecar must be removed after a failed update")
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 || dyn[0].Runtime != "go" || dyn[0].Version != "1.0.0" {
		t.Errorf("the .go plugin must be registered again after rollback, got %+v", dyn)
	}

	// A stale module already sitting in WasmDir (e.g. dropped by an operator
	// but never loaded) is restored, not deleted, when the migration rolls back.
	if err := os.WriteFile(wasmPath, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := wasm.WriteSidecar(wasmPath, []string{"stale.example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}
	if b, _ := os.ReadFile(wasmPath); string(b) != "stale" {
		t.Errorf("a pre-existing module must be restored after a failed migration, got %q", b)
	}
	if hosts, _ := wasm.ReadSidecar(wasmPath); len(hosts) != 1 || hosts[0] != "stale.example.test" {
		t.Errorf("a pre-existing sidecar must be restored after a failed migration, got %v", hosts)
	}
	for _, leftover := range []string{wasmPath + ".prev", wasm.SidecarPath(wasmPath) + ".prev"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("no %s should remain after a rolled-back migration", filepath.Base(leftover))
		}
	}

	inst.hookBeforeRegister = nil
	res, err := inst.Update(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(goPath); err == nil {
		t.Error("the .go file must be removed once the wasm module is registered")
	}
	if b, _ := os.ReadFile(wasmPath); !bytes.Equal(b, f.echoWasm) {
		t.Error("echo.wasm should hold the module bytes")
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 || dyn[0].Runtime != "wasm" || dyn[0].Path != wasmPath {
		t.Errorf("dynamic = %+v", dyn)
	}
}

func TestInstall_HookFailure(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst := newInstaller(t, f)
	inst.hookBeforeRegister = func(plugin.Plugin) error { return errors.New("simulated init failure") }

	if _, err := inst.Install(context.Background(), "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}
	entries, _ := os.ReadDir(inst.WasmDir)
	if len(entries) != 0 {
		t.Errorf("nothing should be left in WasmDir when the register hook fails, got %v", entries)
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing should be registered when the register hook fails")
	}
}

func TestUpdate_RollbackOnRegisterFailure(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", []string{"a.example.test"})}
	inst := newInstaller(t, f)
	ctx := context.Background()
	if _, err := inst.Install(ctx, "echo"); err != nil {
		t.Fatal(err)
	}
	inst.Registry.UpdateSetting("echo", "greeting", "keep me")
	path := filepath.Join(inst.WasmDir, "echo.wasm")

	// Index now offers 1.2.4.
	f.entries = []map[string]any{f.wasmEntryV2([]string{"b.example.test"})}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}

	// The module cannot be made to fail OnInit on demand, so this simulates
	// the failure that would normally come from RegisterDynamic/InitPlugin,
	// to exercise the rename→unregister→write→register→rollback sequence
	// end to end.
	inst.hookBeforeRegister = func(plugin.Plugin) error { return errors.New("simulated init failure") }
	if _, err := inst.Update(ctx, "echo"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}

	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, f.echoWasm) {
		t.Fatalf("echo.wasm should hold the 1.2.3 bytes after rollback: %v", err)
	}
	if hosts, _ := wasm.ReadSidecar(path); len(hosts) != 1 || hosts[0] != "a.example.test" {
		t.Errorf("the previous sidecar must be restored after rollback, got %v", hosts)
	}
	for _, leftover := range []string{path + ".prev", wasm.SidecarPath(path) + ".prev"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("no %s should remain after a rolled-back update", filepath.Base(leftover))
		}
	}
	dyn := inst.Registry.Dynamic()
	if len(dyn) != 1 || dyn[0].Version != "1.2.3" {
		t.Errorf("the previous plugin must be registered again after rollback, got %+v", dyn)
	}
	if setting(inst.Registry, "echo", "greeting") != "keep me" {
		t.Error("settings must survive a rolled-back update")
	}
	// The previous instance was never closed, so it still answers.
	if got := footer(inst.Registry, "echo"); got != "<p>alive</p>" {
		t.Errorf("the restored instance should answer hooks, got %q", got)
	}

	// Clearing the hook lets a subsequent update through.
	inst.hookBeforeRegister = nil
	res, err := inst.Update(ctx, "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.2.4" {
		t.Errorf("update after clearing the hook should succeed, got %+v", res)
	}
}

func TestInstall_ConcurrentSameName(t *testing.T) {
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst := newInstaller(t, f)
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]error, 2)
	for idx := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := inst.Install(ctx, "echo")
			results[idx] = err
		}(idx)
	}
	wg.Wait()

	successes, already := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyInstalled):
			already++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 || already != 1 {
		t.Errorf("expected exactly one success and one ErrAlreadyInstalled, got %d successes, %d already-installed: %+v", successes, already, results)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "echo.wasm")); err != nil {
		t.Errorf("file should exist after concurrent installs: %v", err)
	}
	if dyn := inst.Registry.Dynamic(); len(dyn) != 1 {
		t.Errorf("expected exactly one dynamic entry, got %+v", dyn)
	}
}

func TestInstall_InvalidName(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	if _, err := inst.Install(context.Background(), "../x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	for _, dir := range []string{inst.Dir, inst.WasmDir} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("nothing should be written for an invalid name, got %v", entries)
		}
	}
}

func TestInstall_RefusesRedirectOffHTTPS(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	e := f.wasmEntry("redir", "1.0.0", nil)
	e["download_url"] = f.srv.URL + "/redirect"
	f.entries = []map[string]any{e}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}

	_, err := inst.Install(context.Background(), "redir")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("expected an https-related error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.WasmDir, "redir.wasm")); err == nil {
		t.Error("nothing should be written when the download redirects off https")
	}
}

// TestStatus_StaleIndexRefetchesWithoutExplicitRefresh checks that ensureIndex
// forces a refresh once the cached index is older than staleAfter, not just
// when the configured URL changes — otherwise Fetcher.Ensure only ever fetches
// once (while the cache is empty) and "update available" / new directory
// entries would only ever appear after an operator clicks the manual refresh.
func TestStatus_StaleIndexRefetchesWithoutExplicitRefresh(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)

	orig := staleAfter
	staleAfter = 0
	t.Cleanup(func() { staleAfter = orig })

	inst.Status()
	inst.Status()
	if n := f.hits.Load(); n != 2 {
		t.Errorf("with staleAfter=0 two Status() calls should each refresh, got %d hits", n)
	}
}

// TestStatus_FreshIndexDoesNotRefetch is the control for the test above: with
// the default staleAfter, a second Status() call right after the first must
// not re-fetch the index.
func TestStatus_FreshIndexDoesNotRefetch(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)

	inst.Status()
	inst.Status()
	if n := f.hits.Load(); n != 1 {
		t.Errorf("a fresh cache should not be refetched, got %d hits", n)
	}
}

// TestStatus_DirWritability checks that Status() probes plugins/wasm/ for
// writability and surfaces the result, and that Install fails with ErrWrite
// (rather than a generic error) when the directory cannot be written to.
func TestStatus_DirWritability(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks do not apply")
	}
	f := newFixture(t)
	f.entries = []map[string]any{f.wasmEntry("echo", "1.2.3", nil)}
	inst := newInstaller(t, f)
	dir := inst.WasmDir
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0755) })

	st := inst.Status()
	if st.DirWritable {
		t.Error("expected DirWritable to be false for a read-only directory")
	}
	if st.DirError == "" {
		t.Error("expected DirError to be set for a read-only directory")
	}

	if _, err := inst.Install(context.Background(), "echo"); !errors.Is(err, ErrWrite) {
		t.Errorf("expected ErrWrite installing into a read-only directory, got %v", err)
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing may be registered when the module cannot be written")
	}
}

func TestStatus_IndexURLChangeRefetchesWithoutExplicitRefresh(t *testing.T) {
	f1 := newFixture(t)
	inst := newInstaller(t, f1)
	st := inst.Status()
	if len(st.Available) == 0 {
		t.Fatal("expected entries from the first index")
	}

	f2 := newFixture(t)
	f2.entries = []map[string]any{f2.wasmEntry("other", "1.0.0", nil)}
	inst.IndexURL = func() string { return f2.srv.URL + "/index.json" }

	st = inst.Status()
	if len(st.Available) != 1 || st.Available[0].Name != "other" {
		t.Errorf("expected the second index's entries without an explicit Refresh, got %+v", st.Available)
	}
}

// compiledPlugin stands in for a compiled-in plugin named like an index entry.
type compiledPlugin struct{ plugin.BasePlugin }

func (compiledPlugin) Name() string        { return "scholar" }
func (compiledPlugin) DisplayName() string { return "Scholar" }
func (compiledPlugin) Version() string     { return "1.0.0" }
