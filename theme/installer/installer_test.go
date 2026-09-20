package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"
	"goblog/theme"

	pinstaller "goblog/plugin/installer"
)

func zipOf(t *testing.T, prefix string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

type harness struct {
	inst        *Installer
	srv         *httptest.Server
	index       []directory.Entry
	zips        map[string][]byte // path → archive
	active      string
	activations []string
	root        string
}

// newHarness serves an index with "ocean" v1.0.0 (a valid theme) and
// "broken" (a theme whose template does not parse), points the theme
// roots at temp dirs with a default theme, and wires an Installer.
func newHarness(t *testing.T) *harness {
	t.Helper()
	builtin, installed := t.TempDir(), t.TempDir()
	old := theme.BuiltinRoot
	theme.BuiltinRoot = builtin
	t.Cleanup(func() { theme.BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	shared := t.TempDir()
	oldShared := theme.SharedDir
	theme.SharedDir = shared
	t.Cleanup(func() { theme.SharedDir = oldShared })
	os.WriteFile(filepath.Join(shared, "shared.html"), []byte(`{{ define "shared" }}S{{ end }}`), 0o644)
	os.MkdirAll(filepath.Join(builtin, "default", "templates"), 0o755)
	os.WriteFile(filepath.Join(builtin, "default", "templates", "home.html"), []byte("default"), 0o644)
	os.MkdirAll(filepath.Join(builtin, "forest", "templates"), 0o755)

	h := &harness{active: "default", root: installed}
	oceanFiles := map[string][]byte{"templates/home.html": []byte("ocean"), "static/css/o.css": []byte("c")}
	h.zips = map[string][]byte{
		"/o/ocean/archive/refs/tags/v1.0.0.zip":  zipOf(t, "ocean-1.0.0/", map[string]string{"templates/home.html": "ocean", "static/css/o.css": "c", "README.md": "r"}),
		"/o/ocean/archive/refs/tags/v1.1.0.zip":  zipOf(t, "ocean-1.1.0/", map[string]string{"templates/home.html": "ocean 2"}),
		"/o/broken/archive/refs/tags/v1.0.0.zip": zipOf(t, "broken-1.0.0/", map[string]string{"templates/home.html": "{{ if }}"}),
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/themes/index.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(h.index)
			return
		}
		if z, ok := h.zips[r.URL.Path]; ok {
			w.Write(z)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.srv.Close)
	entry := func(name, version string, files map[string][]byte, min string) directory.Entry {
		return directory.Entry{Kind: registry.KindTheme, Name: name, DisplayName: strings.ToUpper(name), Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: h.srv.URL + "/o/" + name + "/archive/refs/tags/v" + version + ".zip",
			SHA256: registry.ContentHash(files), MinGoblogVersion: min, InstallType: "theme", AllowedHosts: []string{}, Stars: 1}
	}
	h.index = []directory.Entry{
		entry("ocean", "1.0.0", oceanFiles, "0.5.0"),
		entry("broken", "1.0.0", map[string][]byte{"templates/home.html": []byte("{{ if }}")}, "0.5.0"),
		entry("future", "1.0.0", oceanFiles, "9.0.0"),
		{Kind: registry.KindPlugin, Name: "hello", Version: "1.0.0", InstallType: "wasm", DownloadURL: h.srv.URL + "/x"},
	}
	h.inst = &Installer{
		Dir: installed, Directory: pinstaller.NewFetcher(h.srv.Client()), Version: "v0.5.0", Client: h.srv.Client(),
		IndexURL:    func() string { return h.srv.URL + "/themes/index.json" },
		ActiveTheme: func() string { return h.active },
		Activate:    func(name string) error { h.activations = append(h.activations, name); h.active = name; return nil },
	}
	return h
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	st := h.inst.Status()
	if st.Active != "default" || !st.DirWritable || st.IndexError != "" {
		t.Errorf("status = %+v", st)
	}
	names := map[string]Installed{}
	for _, i := range st.Installed {
		names[i.Name] = i
	}
	if !names["default"].Builtin || !names["default"].Active || !names["forest"].Builtin || len(names) != 2 {
		t.Errorf("installed = %+v", st.Installed)
	}
	avail := map[string]Available{}
	for _, a := range st.Available {
		avail[a.Name] = a
	}
	if len(avail) != 3 || !avail["ocean"].Compatible || avail["future"].Compatible || avail["future"].Reason == "" {
		t.Errorf("available = %+v", st.Available)
	}
	if _, ok := avail["hello"]; ok {
		t.Error("plugins in the index must be ignored")
	}
}

func TestInstallActivateUpdateUninstall(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res, err := h.inst.Install(ctx, "ocean")
	if err != nil || res.Version != "1.0.0" {
		t.Fatalf("install: %+v %v", res, err)
	}
	if b, err := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); err != nil || string(b) != "ocean" {
		t.Errorf("template not written: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean", "README.md")); err == nil {
		t.Error("only templates/ and static/ are installed")
	}
	var m registry.ThemeManifest
	if b, err := os.ReadFile(filepath.Join(h.root, "ocean", "goblog-theme.json")); err != nil || json.Unmarshal(b, &m) != nil || m.Name != "ocean" {
		t.Errorf("manifest not written: %v", err)
	}
	if _, err := h.inst.Install(ctx, "ocean"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("install twice: %v", err)
	}
	st := h.inst.Status()
	var ocean Installed
	for _, i := range st.Installed {
		if i.Name == "ocean" {
			ocean = i
		}
	}
	if ocean.Version != "1.0.0" || ocean.Builtin || ocean.UpdateAvailable {
		t.Errorf("ocean status = %+v", ocean)
	}

	if err := h.inst.ActivateTheme("ocean"); err != nil || h.active != "ocean" {
		t.Errorf("activate: %v active=%s", err, h.active)
	}
	if err := h.inst.ActivateTheme("nope"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("activate unknown: %v", err)
	}
	if err := h.inst.Uninstall("ocean"); !errors.Is(err, ErrActive) {
		t.Errorf("uninstall active: %v", err)
	}

	// A newer release appears in the index.
	h.index[0].Version = "1.1.0"
	h.index[0].DownloadURL = h.srv.URL + "/o/ocean/archive/refs/tags/v1.1.0.zip"
	h.index[0].SHA256 = registry.ContentHash(map[string][]byte{"templates/home.html": []byte("ocean 2")})
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	st = h.inst.Status()
	for _, i := range st.Installed {
		if i.Name == "ocean" && (!i.UpdateAvailable || i.LatestVersion != "1.1.0") {
			t.Errorf("update not detected: %+v", i)
		}
	}
	if _, err := h.inst.Update(ctx, "ocean"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "ocean 2" {
		t.Errorf("update not applied: %q", b)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean", "static")); err == nil {
		t.Error("update replaces the whole theme, stale files must not linger")
	}
	if h.activations[len(h.activations)-1] != "ocean" {
		t.Error("updating the active theme must reload it")
	}
	if _, err := h.inst.Update(ctx, "ocean"); !errors.Is(err, ErrUpToDate) {
		t.Errorf("update at latest: %v", err)
	}

	h.active = "default"
	if err := h.inst.Uninstall("ocean"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "ocean")); err == nil {
		t.Error("uninstall must remove the directory")
	}
	if err := h.inst.Uninstall("forest"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("uninstall built-in: %v", err)
	}
	if err := h.inst.Uninstall("ocean"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("uninstall missing: %v", err)
	}
}

func TestInstallRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.inst.Install(ctx, "future"); !errors.Is(err, ErrIncompatible) {
		t.Errorf("incompatible: %v", err)
	}
	if _, err := h.inst.Install(ctx, "forest"); !errors.Is(err, ErrBuiltin) {
		t.Errorf("built-in: %v", err)
	}
	if _, err := h.inst.Install(ctx, "hello"); !errors.Is(err, ErrNotTheme) {
		t.Errorf("plugin entry: %v", err)
	}
	if _, err := h.inst.Install(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := h.inst.Install(ctx, "broken"); !errors.Is(err, ErrLoad) {
		t.Errorf("broken templates: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "broken")); err == nil {
		t.Error("a failed install must leave nothing behind")
	}
	h.index[0].SHA256 = "00"
	h.inst.Refresh()
	if _, err := h.inst.Install(ctx, "ocean"); !errors.Is(err, ErrChecksum) {
		t.Errorf("hash mismatch: %v", err)
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 0 {
		t.Errorf("nothing may be written on a refused install, found %v", entries)
	}
}

func TestUpdateRollsBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.inst.Install(ctx, "ocean"); err != nil {
		t.Fatal(err)
	}
	h.index[0].Version = "1.1.0"
	h.index[0].DownloadURL = h.srv.URL + "/o/ocean/archive/refs/tags/v1.1.0.zip"
	h.index[0].SHA256 = "00" // wrong hash: the download is refused
	h.inst.Refresh()
	if _, err := h.inst.Update(ctx, "ocean"); !errors.Is(err, ErrChecksum) {
		t.Errorf("update: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "ocean" {
		t.Errorf("previous theme must survive a failed update: %q", b)
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 1 {
		t.Errorf("no temp/prev dirs may linger: %v", entries)
	}
}
