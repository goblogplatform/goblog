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
