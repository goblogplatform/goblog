package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// /slow serves ocean v1.0.0 but first reports on slowStarted and then
	// waits for release(), so a test can act while a download is in
	// flight.
	slowStarted chan struct{}
	slowRelease chan struct{}
	releaseOnce sync.Once
}

// release lets every gated /slow download proceed; the harness also calls
// it at cleanup so a failed assertion does not hang on the server's close.
func (h *harness) release() { h.releaseOnce.Do(func() { close(h.slowRelease) }) }

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
	os.MkdirAll(filepath.Join(builtin, "minimal", "templates"), 0o755)

	h := &harness{active: "default", root: installed, slowStarted: make(chan struct{}, 2), slowRelease: make(chan struct{})}
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
		if r.URL.Path == "/slow" {
			h.slowStarted <- struct{}{}
			<-h.slowRelease
			w.Write(h.zips["/o/ocean/archive/refs/tags/v1.0.0.zip"])
			return
		}
		if r.URL.Path == "/loop" {
			// Redirects to itself forever, for the redirect-hop-limit test.
			http.Redirect(w, r, "/loop", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.srv.Close)
	t.Cleanup(h.release)
	entry := func(name, version string, files map[string][]byte, min string) directory.Entry {
		return directory.Entry{Kind: registry.KindTheme, Name: name, DisplayName: strings.ToUpper(name), Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: h.srv.URL + "/o/" + name + "/archive/refs/tags/v" + version + ".zip",
			SHA256: registry.ContentHash(files), MinGoblogVersion: min, InstallType: "theme", AllowedHosts: []string{}, Stars: 1,
			ScreenshotURL: "https://raw.test/o/" + name + "/screenshot.png"}
	}
	h.index = []directory.Entry{
		entry("ocean", "1.0.0", oceanFiles, "0.5.0"),
		entry("broken", "1.0.0", map[string][]byte{"templates/home.html": []byte("{{ if }}")}, "0.5.0"),
		entry("future", "1.0.0", oceanFiles, "9.0.0"),
		{Kind: registry.KindPlugin, Name: "hello", Version: "1.0.0", InstallType: "wasm", DownloadURL: h.srv.URL + "/x"},
		// A theme-kind entry whose install_type is not "theme": Status must
		// mark it incompatible rather than letting Install fail on it later.
		{Kind: registry.KindTheme, Name: "mismatched", DisplayName: "MISMATCHED", Description: "d", Version: "1.0.0", Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/mismatched", DownloadURL: h.srv.URL + "/x", SHA256: "00", MinGoblogVersion: "0.5.0", InstallType: "wasm", AllowedHosts: []string{}, Stars: 1},
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
	if !names["default"].Builtin || !names["default"].Active || !names["minimal"].Builtin || len(names) != 2 {
		t.Errorf("installed = %+v", st.Installed)
	}
	avail := map[string]Available{}
	for _, a := range st.Available {
		avail[a.Name] = a
	}
	if len(avail) != 4 || !avail["ocean"].Compatible || avail["future"].Compatible || avail["future"].Reason == "" {
		t.Errorf("available = %+v", st.Available)
	}
	if avail["mismatched"].Compatible || avail["mismatched"].Reason == "" {
		t.Errorf("theme-kind entry with a non-theme install_type must be marked incompatible: %+v", avail["mismatched"])
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
	if info, err := os.Stat(filepath.Join(h.root, "ocean")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("theme dir mode = %v (err %v), want 0755", info, err)
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
	// The installed row carries the directory's screenshot so the admin page
	// can show installed themes as cards; built-ins have none.
	if ocean.ScreenshotURL != "https://raw.test/o/ocean/screenshot.png" {
		t.Errorf("ocean screenshot = %q", ocean.ScreenshotURL)
	}
	for _, i := range st.Installed {
		if i.Builtin && i.ScreenshotURL != "" {
			t.Errorf("built-in %s must not carry a screenshot: %+v", i.Name, i)
		}
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
	if err := h.inst.Uninstall("minimal"); !errors.Is(err, ErrBuiltin) {
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
	if _, err := h.inst.Install(ctx, "minimal"); !errors.Is(err, ErrBuiltin) {
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

func TestInstallRefusesRedirectLoop(t *testing.T) {
	h := newHarness(t)
	h.index = append(h.index, directory.Entry{
		Kind: registry.KindTheme, Name: "loopy", DisplayName: "LOOPY", Description: "d", Version: "1.0.0", Author: "a", License: "MIT",
		SourceURL: "https://github.com/o/loopy", DownloadURL: h.srv.URL + "/loop", SHA256: "00",
		MinGoblogVersion: "0.5.0", InstallType: "theme", AllowedHosts: []string{}, Stars: 1,
	})
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := h.inst.Install(context.Background(), "loopy"); !errors.Is(err, ErrDownload) {
		t.Errorf("redirect loop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("install took %v; the redirect-hop limit should stop it promptly, not wait out the client timeout", elapsed)
	}
}

// lyingZip returns an archive holding one entry of size bytes whose central
// directory declares it as only 16 bytes, the way a crafted archive would
// try to slip a large file past a size check that trusts the header.
func lyingZip(t *testing.T, name string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, size)) // zeros deflate to almost nothing
	w.Close()
	z := buf.Bytes()
	// Central directory file header: signature, then 20 bytes of fixed
	// fields, then uncompressed size at offset 24.
	cd := bytes.Index(z, []byte{0x50, 0x4b, 0x01, 0x02})
	if cd < 0 {
		t.Fatal("no central directory in the archive")
	}
	binary.LittleEndian.PutUint32(z[cd+24:], 16)
	return z
}

func TestInstallRejectsArchiveWithLyingSizes(t *testing.T) {
	h := newHarness(t)
	h.zips["/o/liar/archive/refs/tags/v1.0.0.zip"] = lyingZip(t, "static/big.bin", registry.MaxAssetBytes+1)
	h.zips["/o/liar-tpl/archive/refs/tags/v1.0.0.zip"] = lyingZip(t, "templates/home.html", registry.MaxTemplateBytes+1)
	for _, name := range []string{"liar", "liar-tpl"} {
		h.index = append(h.index, directory.Entry{
			Kind: registry.KindTheme, Name: name, DisplayName: name, Description: "d", Version: "1.0.0", Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: h.srv.URL + "/o/" + name + "/archive/refs/tags/v1.0.0.zip", SHA256: "00",
			MinGoblogVersion: "0.5.0", InstallType: "theme", AllowedHosts: []string{}, Stars: 1,
		})
	}
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"liar", "liar-tpl"} {
		// ErrLoad, not ErrChecksum: the archive must be refused while it is
		// being read, before its content is hashed or written anywhere.
		if _, err := h.inst.Install(context.Background(), name); !errors.Is(err, ErrLoad) {
			t.Errorf("%s: an entry larger than its header claims must be refused, got %v", name, err)
		}
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 0 {
		t.Errorf("nothing may be written for a refused archive, found %v", entries)
	}
}

func TestUpdateRefusesHandCopiedTheme(t *testing.T) {
	h := newHarness(t)
	// An operator dropped a theme named like a directory one straight into
	// the installed root; it has no manifest, so nothing says which version
	// it is or that the directory copy is the same theme at all.
	os.MkdirAll(filepath.Join(h.root, "ocean", "templates"), 0o755)
	os.WriteFile(filepath.Join(h.root, "ocean", "templates", "home.html"), []byte("hand-copied"), 0o644)
	if _, err := h.inst.Update(context.Background(), "ocean"); !errors.Is(err, ErrNotManaged) {
		t.Errorf("update of a hand-copied theme: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "hand-copied" {
		t.Errorf("a refused update must leave the theme alone, got %q", b)
	}
	if _, err := h.inst.Install(context.Background(), "ocean"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("install over a hand-copied theme: %v", err)
	}
	st := h.inst.Status()
	for _, i := range st.Installed {
		if i.Name == "ocean" && (i.UpdateAvailable || i.Version != "") {
			t.Errorf("a hand-copied theme has no version to update from: %+v", i)
		}
	}
}

func TestInstallDoesNotHoldLockDuringDownload(t *testing.T) {
	h := newHarness(t)
	h.index[0].DownloadURL = h.srv.URL + "/slow"
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := h.inst.Install(context.Background(), "ocean")
		done <- outcome{res, err}
	}()
	select {
	case <-h.slowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("download never started")
	}
	// The archive is still downloading; activating must not queue behind it.
	activated := make(chan error, 1)
	go func() { activated <- h.inst.ActivateTheme(theme.DefaultName) }()
	select {
	case err := <-activated:
		if err != nil {
			t.Errorf("activate during download: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ActivateTheme blocked behind an in-flight download")
	}
	h.release()
	select {
	case o := <-done:
		if o.err != nil || o.res.Version != "1.0.0" {
			t.Errorf("install after release: %+v %v", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("install never finished")
	}
}

func TestInstallRechecksUnderLock(t *testing.T) {
	// Two installs of the same theme both download (the lock is not held
	// then); only one may place it, the other must see ErrAlreadyInstalled
	// rather than replacing what the first wrote.
	h := newHarness(t)
	h.index[0].DownloadURL = h.srv.URL + "/slow"
	if err := h.inst.Refresh(); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := h.inst.Install(context.Background(), "ocean")
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-h.slowStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("both downloads should be in flight at once")
		}
	}
	h.release()
	var ok, already int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrAlreadyInstalled):
			already++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || already != 1 {
		t.Errorf("got %d successes and %d already-installed", ok, already)
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 1 {
		t.Errorf("exactly one theme dir may remain: %v", entries)
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t)
	mk := func(rel, body string) {
		t.Helper()
		full := filepath.Join(h.root, rel)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A crash between the two renames: the old copy is set aside and the
	// new one never made it. The theme must come back.
	mk("ocean.prev/templates/home.html", "old ocean")
	mk(".ocean.tmp-123456/templates/home.html", "new ocean")
	// A crash after the swap but before the .prev was dropped: the new
	// copy is in place and wins.
	mk("sky/templates/home.html", "new sky")
	mk("sky.prev/templates/home.html", "old sky")
	// An unpack that never got as far as a swap.
	mk(".dune.tmp-a1B2c3/templates/home.html", "partial") // any MkdirTemp suffix, not only digits
	// Not the installer's: an installed theme, a hidden directory with a
	// different shape, and a plain file that happens to end in .prev.
	mk("prairie/templates/home.html", "prairie")
	mk(".git/HEAD", "ref")
	mk("notes.prev", "a file")

	if err := h.inst.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "ocean", "templates", "home.html")); string(b) != "old ocean" {
		t.Errorf("ocean.prev must be renamed back when ocean is missing, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "sky", "templates", "home.html")); string(b) != "new sky" {
		t.Errorf("sky must be left alone, got %q", b)
	}
	for _, gone := range []string{"ocean.prev", "sky.prev", ".ocean.tmp-123456", ".dune.tmp-a1B2c3"} {
		if _, err := os.Stat(filepath.Join(h.root, gone)); err == nil {
			t.Errorf("%s must be removed by the sweep", gone)
		}
	}
	for _, kept := range []string{"prairie", ".git/HEAD", "notes.prev"} {
		if _, err := os.Stat(filepath.Join(h.root, kept)); err != nil {
			t.Errorf("%s must survive the sweep: %v", kept, err)
		}
	}
	// A root that does not exist yet is not an error: nothing was ever
	// installed there.
	h.inst.Dir = filepath.Join(t.TempDir(), "missing")
	if err := h.inst.Sweep(); err != nil {
		t.Errorf("sweep of a missing root: %v", err)
	}
}
