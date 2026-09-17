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
	"sync"
	"time"

	"goblog/plugin"
	"goblog/plugins/directory"
)

// DefaultIndexURL is the public plugin directory index.
const DefaultIndexURL = "https://www.goblog.live/plugins/index.json"

// maxPluginBytes caps a plugin download.
const maxPluginBytes = 1 << 20

// staleAfter is how long a cached directory index is served before
// ensureIndex forces a refresh even though something is already cached.
// Without this, Fetcher.Ensure only ever fetches once (when the cache is
// empty), so "update available" and new directory entries would only ever
// appear after an operator clicks the manual ↻ refresh. A package-level var
// so tests can force it to 0 and observe every Status() call refreshing.
var staleAfter = time.Hour

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
	ErrUpToDate             = errors.New("plugin is already at the directory's version")
	ErrDownload             = errors.New("could not download the plugin")
	ErrWrite                = errors.New("could not write to the plugins/dynamic directory; check that it exists and goblog can write to it")
)

// Installer wires the directory index, the plugin registry and the
// plugins/dynamic directory together.
type Installer struct {
	Dir       string // plugins/dynamic
	Registry  *plugin.Registry
	Directory *directory.Fetcher // own instance; not the directory plugin's
	Version   string             // running goblog version, e.g. "v0.2.7" or "development"
	Client    *http.Client       // downloads; nil → 30s timeout default
	Enabled   bool               // ENABLE_DYNAMIC_PLUGINS
	IndexURL  func() string      // current plugin_directory_url setting

	// mu serializes Install/Update/Uninstall so two callers acting on the
	// same (or different) plugin names cannot interleave writes to the
	// dynamic directory or the registry. Status and Refresh do not take it.
	mu sync.Mutex

	// urlMu guards lastURL, the index URL last fetched by ensureIndex.
	urlMu   sync.Mutex
	lastURL string

	// hookBeforeRegister is a test-only seam: when set, register() calls it
	// before touching the registry and, on error, fails the install/update
	// as if the plugin had failed to load. Dynamic plugins loaded through
	// Yaegi cannot be made to fail OnInit on demand, so tests use this hook
	// to exercise the rollback path instead.
	hookBeforeRegister func(p plugin.Plugin) error
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
	DirWritable    bool        `json:"dir_writable"`
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
	st := Status{DirectoryURL: indexURL, DynamicEnabled: i.Enabled, Installed: []Installed{}, Available: []Available{}}
	if !ok {
		st.IndexError = "could not fetch the plugin directory index"
	} else {
		st.IndexFetchedAt = i.Directory.FetchedAt().UTC().Format(time.RFC3339)
	}
	if i.Enabled {
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
	for _, e := range validEntries {
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
// file: name validity, dynamic, compatible.
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
	i.mu.Lock()
	defer i.mu.Unlock()
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
		return Result{}, fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if err := i.register(p, path); err != nil {
		// A concurrent RegisterDynamic under the same name won the race;
		// that plugin owns the file now, so it must not be removed.
		if !errors.Is(err, ErrAlreadyInstalled) {
			os.Remove(path)
		}
		return Result{}, err
	}
	log.Printf("Installed plugin %s v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Installed %s v%s", e.DisplayName, e.Version)}, nil
}

// Update replaces an installed dynamic plugin with the index version. If the
// new file fails to load, the previous file and plugin are restored.
func (i *Installer) Update(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
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
	prev := d.Path + ".prev"
	if err := os.Rename(d.Path, prev); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrWrite, err)
	}
	rollback := func(cause error) (Result, error) {
		i.Registry.Unregister(name) // no-op if the new plugin never registered
		if err := os.Rename(prev, d.Path); err != nil {
			log.Printf("Update %s: restoring %s failed: %v", name, d.Path, err)
			cause = fmt.Errorf("%w; restoring the previous file also failed: %v", cause, err)
		}
		if old != nil {
			if err := i.Registry.RegisterDynamic(old, d.Path); err == nil {
				// InitPlugin deliberately re-runs OnInit for the restored
				// previous version, so any scheduled jobs it starts there
				// come back up. Dynamic plugins loaded through Yaegi can't
				// override OnInit today (it names gorm types the
				// interpreter doesn't expose), so this is a no-op for them
				// in practice, but it still matters if that ever changes.
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
		return rollback(fmt.Errorf("%w: %v", ErrWrite, err))
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
	i.mu.Lock()
	defer i.mu.Unlock()
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
		return fmt.Errorf("%w: %v", ErrWrite, err)
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
func (i *Installer) download(ctx context.Context, rawURL string) ([]byte, error) {
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
	src, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if len(src) > maxPluginBytes {
		return nil, fmt.Errorf("%w: file is larger than %d bytes", ErrLoad, maxPluginBytes)
	}
	return src, nil
}

// probeDirWritable creates i.Dir if needed and proves goblog can write to
// it by creating and removing a temp file, without leaving anything behind.
// Surfacing this in Status lets the admin page explain an otherwise-generic
// write failure (e.g. a read-only bind mount) before anyone clicks Install.
func (i *Installer) probeDirWritable() (bool, string) {
	if err := os.MkdirAll(i.Dir, 0755); err != nil {
		return false, err.Error()
	}
	f, err := os.CreateTemp(i.Dir, ".probe-*")
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
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
