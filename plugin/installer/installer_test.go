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
	"sync"
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
		case "/redirect":
			http.Redirect(w, r, "http://example.invalid/x.go", http.StatusFound)
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
		"name": name, "display_name": capitalize(name), "description": "d", "version": version,
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

	bad := f.entry("bad name", "1.0.0", "/hello.go", "0.2.6", 5)
	unsafe := f.entry("unsafe", "1.0.0", "/hello.go", "0.2.6", 1)
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
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrUpToDate) {
		t.Errorf("updating when already at the index version should be ErrUpToDate, got %v", err)
	}

	// Rollback: the index advertises 1.2.0 but serves a file whose Version() is 1.1.0.
	// This is rejected in fetchAndCheck before anything is touched on disk or in
	// the registry, so it does not exercise the rename→unregister→rollback path;
	// TestUpdate_RollbackOnRegisterFailure below does, via hookBeforeRegister.
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

func TestInstall_HookFailure(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	inst.hookBeforeRegister = func(plugin.Plugin) error { return errors.New("simulated init failure") }

	if _, err := inst.Install(context.Background(), "hello"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go")); err == nil {
		t.Error("nothing should be written when the register hook fails")
	}
	if len(inst.Registry.Dynamic()) != 0 {
		t.Error("nothing should be registered when the register hook fails")
	}
}

func TestUpdate_RollbackOnRegisterFailure(t *testing.T) {
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

	// Dynamic plugins loaded through Yaegi can't be made to fail OnInit on
	// demand, so this simulates the failure that would normally come from
	// RegisterDynamic/InitPlugin, to exercise the rename→unregister→write→
	// register→rollback sequence end to end.
	inst.hookBeforeRegister = func(plugin.Plugin) error { return errors.New("simulated init failure") }
	if _, err := inst.Update(ctx, "hello"); !errors.Is(err, ErrLoad) {
		t.Errorf("expected ErrLoad, got %v", err)
	}

	path := filepath.Join(inst.Dir, "hello.go")
	if b, err := os.ReadFile(path); err != nil || string(b) != string(f.helloV1) {
		t.Fatalf("hello.go should hold the 1.0.0 bytes after rollback: %v", err)
	}
	if _, err := os.Stat(path + ".prev"); err == nil {
		t.Error("no .prev file should remain after a rolled-back update")
	}
	dyn := inst.Registry.Dynamic()
	if len(dyn) != 1 || dyn[0].Version != "1.0.0" {
		t.Errorf("the previous plugin must be registered again after rollback, got %+v", dyn)
	}
	kept := false
	for _, g := range inst.Registry.GetAllSettings() {
		if g.PluginName == "hello" && g.CurrentValues["message"] == "keep me" {
			kept = true
		}
	}
	if !kept {
		t.Error("settings must survive a rolled-back update")
	}

	// Clearing the hook lets a subsequent update through.
	inst.hookBeforeRegister = nil
	res, err := inst.Update(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "1.1.0" {
		t.Errorf("update after clearing the hook should succeed, got %+v", res)
	}
}

func TestInstall_ConcurrentSameName(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make([]error, 2)
	for idx := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := inst.Install(ctx, "hello")
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
	if _, err := os.Stat(filepath.Join(inst.Dir, "hello.go")); err != nil {
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
	entries, _ := os.ReadDir(inst.Dir)
	if len(entries) != 0 {
		t.Errorf("nothing should be written for an invalid name, got %v", entries)
	}
}

func TestInstall_RefusesRedirectOffHTTPS(t *testing.T) {
	f := newFixture(t)
	inst := newInstaller(t, f)
	e := f.entry("redir", "1.0.0", "/hello.go", "0.2.6", 0)
	e["download_url"] = f.srv.URL + "/redirect"
	f.entries = []map[string]any{e}
	if err := inst.Refresh(); err != nil {
		t.Fatal(err)
	}

	_, err := inst.Install(context.Background(), "redir")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("expected an https-related error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, "redir.go")); err == nil {
		t.Error("nothing should be written when the download redirects off https")
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
	f2.entries = []map[string]any{f2.entry("other", "1.0.0", "/hello.go", "0.2.6", 0)}
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
