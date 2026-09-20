// Package installer installs, updates and removes WebAssembly plugins from
// the plugin directory at runtime, for the admin UI. Yaegi (.go) plugins
// that were installed before the directory went wasm-only can still be
// updated (to a wasm release) and uninstalled.
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
	"sync"
	"time"

	"goblog/plugin"
	"goblog/plugin/wasm"
	"goblog/plugins/directory"
)

// DefaultIndexURL is the public plugin directory index.
const DefaultIndexURL = "https://www.goblog.live/plugins/index.json"

// maxPluginBytes caps a plugin download; wasm modules get maxWasmBytes.
const (
	maxPluginBytes = 1 << 20
	maxWasmBytes   = 16 << 20
)

// staleAfter is how long a cached directory index is served before
// ensureIndex forces a refresh even though something is already cached.
// Without this, Fetcher.Ensure only ever fetches once (when the cache is
// empty), so "update available" and new directory entries would only ever
// appear after an operator clicks the manual ↻ refresh. A package-level var
// so tests can force it to 0 and observe every Status() call refreshing.
var staleAfter = time.Hour

// Typed errors; the admin API maps them to 4xx responses with their text.
var (
	ErrWasmDisabled         = errors.New("WebAssembly plugins are disabled (ENABLE_WASM_PLUGINS=false); remove that setting and make plugins/wasm/ writable to install plugins")
	ErrIncompatible         = errors.New("this plugin requires a newer goblog")
	ErrChecksum             = errors.New("the downloaded file does not match the checksum in the directory index; the index may be stale, refresh and try again")
	ErrNotDynamic           = errors.New("this plugin type cannot be installed from the directory")
	ErrAlreadyInstalled     = errors.New("a plugin with this name is already installed")
	ErrNotInstalled         = errors.New("this plugin is not installed as a dynamic plugin")
	ErrNotFound             = errors.New("this plugin is not in the directory")
	ErrLoad                 = errors.New("the plugin failed to load")
	ErrDirectoryUnavailable = errors.New("the plugin directory index is unavailable")
	ErrUpToDate             = errors.New("plugin is already at the directory's version")
	ErrDownload             = errors.New("could not download the plugin")
	ErrWrite                = errors.New("could not write to the plugins/wasm directory; check that it exists and goblog can write to it")
)

// Installer wires the directory index, the plugin registry and the
// plugins/wasm directory together.
type Installer struct {
	Dir         string // plugins/dynamic: previously installed Yaegi plugins (update/uninstall only)
	WasmDir     string // plugins/wasm: where directory installs go
	Registry    *plugin.Registry
	Directory   *Fetcher      // own instance; not the directory plugin's
	Version     string        // running goblog version, e.g. "v0.2.7" or "development"
	Client      *http.Client  // downloads; nil → 30s timeout default
	Enabled     bool          // ENABLE_DYNAMIC_PLUGINS (Yaegi); reported in Status only
	WasmEnabled bool          // ENABLE_WASM_PLUGINS != "false"; gates Install/Update/Uninstall
	IndexURL    func() string // current plugin_directory_url setting

	// mu serializes Install/Update/Uninstall so two callers acting on the
	// same (or different) plugin names cannot interleave writes to the
	// dynamic directory or the registry. Status and Refresh do not take it.
	mu sync.Mutex

	// urlMu guards lastURL, the index URL last fetched by ensureIndex.
	urlMu   sync.Mutex
	lastURL string

	// hookBeforeRegister is a test-only seam: when set, register() calls it
	// before touching the registry and, on error, fails the install/update
	// as if the plugin had failed to load. The test module cannot be made
	// to fail OnInit on demand, so tests use this hook to exercise the
	// rollback path instead.
	hookBeforeRegister func(p plugin.Plugin) error
}

// Installed is a registered plugin as shown on the Installed tab.
type Installed struct {
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version"`
	Enabled         bool   `json:"enabled"`
	Dynamic         bool   `json:"dynamic"`
	Runtime         string `json:"runtime"` // "wasm", "go" or "builtin"
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
	WasmEnabled    bool        `json:"wasm_enabled"`
	IndexFetchedAt string      `json:"index_fetched_at,omitempty"`
	IndexError     string      `json:"index_error,omitempty"`
	DirWritable    bool        `json:"dir_writable"` // WasmDir (Dir when WasmDir is unset)
	DirError       string      `json:"dir_error,omitempty"`
}

// Result reports a successful install or update.
type Result struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Message string `json:"message"`
}

// allowedScheme is the download_url rule: HTTPS, or plain HTTP to loopback
// (for tests only). Applied to both the initial request and any redirect.
func allowedScheme(u *url.URL) bool {
	host := u.Hostname()
	return u.Scheme == "https" || (u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1"))
}

// safeURL returns u unchanged when it is an absolute http(s) URL, otherwise
// "". Directory data such as source_url ends up in an href on the admin
// page; a scheme like javascript: must never survive into that attribute.
func safeURL(u string) string {
	trimmed := strings.TrimSpace(u)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return trimmed
	}
	return ""
}

// client returns an HTTP client that refuses to follow a redirect off
// https (or off loopback, in tests) — a plugin download_url that 302s to a
// plain-http host must not be able to smuggle the file in that way.
func (i *Installer) client() *http.Client {
	base := i.Client
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !allowedScheme(req.URL) {
			return fmt.Errorf("redirected to %s: download_url must be https", req.URL)
		}
		return nil
	}
	return &c
}

// Refresh re-fetches the index now.
func (i *Installer) Refresh() error {
	if err := i.Directory.Refresh(i.IndexURL()); err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}
	return nil
}

// ensureIndex makes sure the directory index reflects indexURL. When the
// configured URL has changed since the last call, it forces a fresh fetch
// (an operator pointing at a different registry should not wait out
// Ensure's throttle for stale-empty caches). Otherwise, once something is
// cached, Fetcher.Ensure would never fetch again on its own, so a cache
// older than staleAfter is also refreshed here (errors are logged; the old
// copy stays in place and is still served). A fresh-empty cache defers to
// Ensure's own throttled fetch-and-retry behaviour.
func (i *Installer) ensureIndex(indexURL string) {
	i.urlMu.Lock()
	changed := indexURL != i.lastURL
	i.lastURL = indexURL
	i.urlMu.Unlock()
	if changed {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Installer: fetching directory index at %s: %v", indexURL, err)
		}
		return
	}
	if fetchedAt := i.Directory.FetchedAt(); !fetchedAt.IsZero() && time.Since(fetchedAt) > staleAfter {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Installer: refreshing stale directory index at %s: %v", indexURL, err)
		}
		return
	}
	i.Directory.Ensure(indexURL)
}

// Status lists installed plugins (with update info for dynamic ones) and the
// directory entries that are not installed, sorted by stars then name.
func (i *Installer) Status() Status {
	indexURL := i.IndexURL()
	i.ensureIndex(indexURL)
	_, entries, ok := i.Directory.Index()
	st := Status{DirectoryURL: indexURL, DynamicEnabled: i.Enabled, WasmEnabled: i.WasmEnabled, Installed: []Installed{}, Available: []Available{}}
	if !ok {
		st.IndexError = "could not fetch the plugin directory index"
	} else {
		st.IndexFetchedAt = i.Directory.FetchedAt().UTC().Format(time.RFC3339)
	}
	if i.WasmEnabled {
		st.DirWritable, st.DirError = i.probeDirWritable()
	}

	// Drop entries whose name would not pass the filesystem-safety check
	// applied at install time; nothing derived from them (map keys, the
	// Available list) should be trusted otherwise. Sanitize the URLs of the
	// ones that remain — they render into href attributes on the admin page
	// and must never carry a javascript: or other unsafe scheme.
	validEntries := make([]directory.Entry, 0, len(entries))
	for _, e := range entries {
		if !directory.ValidName(e.Name) {
			log.Printf("Installer: directory index entry %q has an invalid plugin name, skipping", e.Name)
			continue
		}
		e.SourceURL = safeURL(e.SourceURL)
		e.DownloadURL = safeURL(e.DownloadURL)
		validEntries = append(validEntries, e)
	}

	byName := make(map[string]directory.Entry, len(validEntries))
	for _, e := range validEntries {
		byName[e.Name] = e
	}
	dynamic := map[string]plugin.DynamicInfo{}
	for _, d := range i.Registry.Dynamic() {
		dynamic[d.Name] = d
	}
	installed := map[string]bool{}
	for _, p := range i.Registry.Plugins() {
		installed[p.Name()] = true
		row := Installed{Name: p.Name(), DisplayName: p.DisplayName(), Version: p.Version(), Enabled: i.Registry.IsPluginEnabled(p.Name()), Runtime: "builtin"}
		if d, ok := dynamic[p.Name()]; ok {
			row.Dynamic = true
			row.Runtime = d.Runtime
			row.Path = d.Path
			if e, ok := byName[p.Name()]; ok {
				row.LatestVersion = e.Version
				row.UpdateAvailable = newer(e.Version, p.Version())
			}
		}
		st.Installed = append(st.Installed, row)
	}
	for _, e := range validEntries {
		if installed[e.Name] {
			continue
		}
		a := Available{Entry: e, Compatible: true}
		switch {
		case e.InstallType != "wasm":
			a.Compatible, a.Reason = false, "not installable from the directory"
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
// file: name validity, wasm, compatible.
func (i *Installer) lookup(name string) (directory.Entry, error) {
	if !directory.ValidName(name) {
		return directory.Entry{}, ErrNotFound
	}
	i.ensureIndex(i.IndexURL())
	if _, _, ok := i.Directory.Index(); !ok {
		return directory.Entry{}, ErrDirectoryUnavailable
	}
	e, ok := i.Directory.Entry(name)
	if !ok {
		return directory.Entry{}, ErrNotFound
	}
	// Defensive: the index says this name, but never let anything that
	// wouldn't pass ValidName reach a filesystem path.
	if !directory.ValidName(e.Name) {
		return directory.Entry{}, fmt.Errorf("%w: invalid plugin name %q", ErrLoad, e.Name)
	}
	if e.InstallType != "wasm" {
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
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.WasmEnabled {
		return Result{}, ErrWasmDisabled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if i.isRegistered(name) {
		return Result{}, ErrAlreadyInstalled
	}
	path := i.wasmPath(e.Name)
	// Nothing registered under this name, yet a module is sitting at its
	// path: an operator-dropped file, or one that failed to load at boot.
	// Overwriting it silently would hide that; refuse and say so.
	if _, err := os.Stat(path); err == nil {
		return Result{}, fmt.Errorf("%w: a file for %q already exists in plugins/wasm; remove it first", ErrAlreadyInstalled, e.Name)
	}
	src, p, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	if err := writeModule(path, src, e.AllowedHosts); err != nil {
		closePlugin(p)
		return Result{}, err
	}
	if err := i.register(p, path); err != nil {
		// A concurrent RegisterDynamic under the same name won the race;
		// that plugin owns the files now, so they must not be removed.
		if !errors.Is(err, ErrAlreadyInstalled) {
			removeModule(path)
		}
		closePlugin(p)
		return Result{}, err
	}
	log.Printf("Installed plugin %s v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Installed %s v%s", e.DisplayName, e.Version)}, nil
}

// Update replaces an installed dynamic plugin with the index version. If the
// new module fails to load, the previous files and plugin are restored. A
// Yaegi plugin (Runtime "go") is replaced by the wasm module: its .go file
// stays until the new plugin is registered and is removed afterwards.
func (i *Installer) Update(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.WasmEnabled {
		return Result{}, ErrWasmDisabled
	}
	d, ok := i.dynamic(name)
	if !ok {
		return Result{}, ErrNotInstalled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if _, ok := parseVersion(d.Version); !ok {
		return Result{}, fmt.Errorf("%w: installed version %q is not semver", ErrUpToDate, d.Version)
	}
	if !newer(e.Version, d.Version) {
		return Result{}, fmt.Errorf("%w: %s is at v%s; the directory has v%s", ErrUpToDate, name, d.Version, e.Version)
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
	// A wasm plugin is updated in place, so its module and sidecar are set
	// aside as .prev first. A .go plugin's file is untouched until the end;
	// anything already sitting at the wasm path is set aside the same way.
	path := d.Path
	inPlace := d.Runtime == "wasm"
	if !inPlace {
		path = i.wasmPath(e.Name)
	}
	backup, err := backupModule(path)
	if err != nil {
		closePlugin(p)
		return Result{}, fmt.Errorf("%w: %v", ErrWrite, err)
	}
	rollback := func(cause error) (Result, error) {
		i.Registry.Unregister(name) // no-op if the new plugin never registered
		closePlugin(p)
		if err := backup.restore(); err != nil {
			log.Printf("Update %s: restoring %s failed: %v", name, path, err)
			cause = fmt.Errorf("%w; restoring the previous file also failed: %v", cause, err)
		}
		if old != nil {
			// The previous instance was never closed, so it is registered
			// again as it was.
			if err := i.Registry.RegisterDynamic(old, d.Path); err != nil {
				log.Printf("plugin %s: rollback could not re-register the previous version: %v", name, err)
				closePlugin(old)
			} else if err := i.Registry.InitPlugin(name); err != nil {
				// InitPlugin deliberately re-runs OnInit for the restored
				// previous version, so any scheduled jobs it starts there
				// come back up.
				log.Printf("Update %s: re-initialising previous version failed: %v", name, err)
			}
		}
		return Result{}, cause
	}
	if err := i.Registry.Unregister(name); err != nil {
		return rollback(err)
	}
	if err := writeModule(path, src, e.AllowedHosts); err != nil {
		return rollback(err)
	}
	if err := i.register(p, path); err != nil {
		return rollback(err)
	}
	backup.discard()
	if !inPlace {
		if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
			log.Printf("Update %s: removing the previous %s failed: %v", name, d.Path, err)
		}
	}
	closePlugin(old)
	log.Printf("Updated plugin %s to v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Updated %s to v%s", e.DisplayName, e.Version)}, nil
}

// Uninstall unregisters a dynamic plugin, releases its instance and deletes
// its files, settings, stored data and the pages it declared.
func (i *Installer) Uninstall(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.WasmEnabled {
		return ErrWasmDisabled
	}
	d, ok := i.dynamic(name)
	if !ok {
		return ErrNotInstalled
	}
	var old plugin.Plugin
	var pageTypes []string
	for _, rp := range i.Registry.Plugins() {
		if rp.Name() == name {
			old = rp
			for _, pg := range rp.Pages() {
				pageTypes = append(pageTypes, pg.PageType)
			}
		}
	}
	if err := i.Registry.Unregister(name); err != nil {
		return err
	}
	closePlugin(old)
	if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if d.Runtime == "wasm" {
		if err := os.Remove(wasm.SidecarPath(d.Path)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
	}
	i.Registry.DeleteSettings(name)
	if err := i.Registry.Store().DeleteAll(name); err != nil {
		log.Printf("Uninstall %s: deleting stored data failed: %v", name, err)
	}
	// The registry created a page row for each page the plugin declared;
	// without the plugin those would stay enabled and in the nav as empty
	// pages.
	if err := i.Registry.DeletePages(pageTypes); err != nil {
		log.Printf("Uninstall %s: deleting its pages failed: %v", name, err)
	}
	log.Printf("Uninstalled plugin %s", name)
	return nil
}

// wasmPath is where a directory plugin's module lives.
func (i *Installer) wasmPath(name string) string {
	return filepath.Join(i.WasmDir, name+".wasm")
}

// closePlugin releases a wasm instance; anything else has nothing to release.
func closePlugin(p plugin.Plugin) {
	if wp, ok := p.(*wasm.Plugin); ok && wp != nil {
		wp.Close()
	}
}

// writeModule records the sidecar and then the module at path, so a module
// on disk always has its allowed hosts next to it. On failure nothing is
// left behind.
func writeModule(path string, src []byte, allowedHosts []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if err := wasm.WriteSidecar(path, allowedHosts); err != nil {
		os.Remove(wasm.SidecarPath(path)) // whatever a partial write left behind
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if err := wasm.WriteAtomic(path, src); err != nil {
		removeModule(path)
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	return nil
}

// removeModule deletes a module and its sidecar, ignoring what is not there.
func removeModule(path string) {
	os.Remove(path)
	os.Remove(wasm.SidecarPath(path))
}

// moduleBackup is whatever was at a module path and its sidecar path, set
// aside as .prev while an update writes there. Either file may be absent
// (an operator-dropped module without a sidecar; a Yaegi plugin being
// migrated, whose wasm path is normally empty); restore brings back exactly
// what was there.
type moduleBackup struct {
	path                  string
	hadModule, hadSidecar bool
}

// backupModule renames path and its sidecar, whichever exist, to .prev.
func backupModule(path string) (*moduleBackup, error) {
	b := &moduleBackup{path: path}
	var err error
	if b.hadModule, err = setAside(path); err != nil {
		return nil, err
	}
	if b.hadSidecar, err = setAside(wasm.SidecarPath(path)); err != nil {
		if b.hadModule {
			os.Rename(path+".prev", path)
		}
		return nil, err
	}
	return b, nil
}

// setAside renames path to path.prev and reports whether there was a file.
func setAside(path string) (bool, error) {
	switch err := os.Rename(path, path+".prev"); {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// restore puts back what was set aside, replacing whatever the update wrote;
// a path that was empty before is emptied again.
func (b *moduleBackup) restore() error {
	var firstErr error
	for _, f := range []struct {
		path string
		had  bool
	}{{b.path, b.hadModule}, {wasm.SidecarPath(b.path), b.hadSidecar}} {
		if !f.had {
			os.Remove(f.path)
			continue
		}
		if err := os.Rename(f.path+".prev", f.path); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// discard removes the .prev files after a successful update.
func (b *moduleBackup) discard() {
	if b.hadModule {
		os.Remove(b.path + ".prev")
	}
	if b.hadSidecar {
		os.Remove(wasm.SidecarPath(b.path) + ".prev")
	}
}

// fetchAndCheck downloads the entry's module and proves it is what the index
// says: checksum, instantiates, and reports the same name and version. The
// returned instance is the one that gets registered; the caller closes it
// on every path that does not. Nothing is written or registered here.
func (i *Installer) fetchAndCheck(ctx context.Context, e directory.Entry) ([]byte, plugin.Plugin, error) {
	limit := maxPluginBytes
	if e.InstallType == "wasm" {
		limit = maxWasmBytes
	}
	src, err := i.download(ctx, e.DownloadURL, limit)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(src)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), e.SHA256) {
		return nil, nil, ErrChecksum
	}
	p, err := wasm.LoadBytes(src, wasm.Options{Store: i.Registry.Store(), AllowedHosts: e.AllowedHosts})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if p.Name() != e.Name {
		p.Close()
		return nil, nil, fmt.Errorf("%w: it reports Name() %q but the directory lists it as %q", ErrLoad, p.Name(), e.Name)
	}
	if p.Version() != e.Version {
		p.Close()
		return nil, nil, fmt.Errorf("%w: it reports Version() %q but the directory lists v%s", ErrLoad, p.Version(), e.Version)
	}
	return src, p, nil
}

func (i *Installer) register(p plugin.Plugin, path string) error {
	if i.hookBeforeRegister != nil {
		if err := i.hookBeforeRegister(p); err != nil {
			return fmt.Errorf("%w: %v", ErrLoad, err)
		}
	}
	if i.isRegistered(p.Name()) {
		// Someone else registered this name first; they own the file now.
		return ErrAlreadyInstalled
	}
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
func (i *Installer) download(ctx context.Context, rawURL string, limit int) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: bad download_url: %v", ErrLoad, err)
	}
	if !allowedScheme(u) {
		return nil, fmt.Errorf("%w: download_url must be https", ErrLoad)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "goblog-installer/"+i.Version)
	resp, err := i.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned HTTP %d", ErrDownload, rawURL, resp.StatusCode)
	}
	src, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if len(src) > limit {
		return nil, fmt.Errorf("%w: file is larger than %d bytes", ErrLoad, limit)
	}
	return src, nil
}

// probeDirWritable creates the install directory (WasmDir, or Dir when no
// WasmDir is configured) if needed and proves goblog can write to it by
// creating and removing a temp file, without leaving anything behind.
// Surfacing this in Status lets the admin page explain an otherwise-generic
// write failure (e.g. a read-only bind mount) before anyone clicks Install.
func (i *Installer) probeDirWritable() (bool, string) {
	dir := i.WasmDir
	if dir == "" {
		dir = i.Dir
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err.Error()
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false, err.Error()
	}
	name := f.Name()
	closeErr := f.Close() // some filesystems report write/permission errors only here
	removeErr := os.Remove(name)
	if closeErr != nil {
		return false, closeErr.Error()
	}
	if removeErr != nil {
		return false, removeErr.Error()
	}
	return true, ""
}
