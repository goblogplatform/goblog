// Package installer installs, updates and removes themes from the theme
// directory at runtime, for the admin UI. A theme is a directory of
// templates and static files: install means download the release archive,
// check its content hash against the index, check the templates parse, and
// unpack it under the installed root.
package installer

import (
	"context"
	"encoding/json"
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

	pinstaller "goblog/plugin/installer"
	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"
	"goblog/theme"
)

// DefaultIndexURL is the public theme directory index.
const DefaultIndexURL = "https://www.goblog.live/themes/index.json"

// maxArchiveBytes caps a theme download (the registry applies the same).
const maxArchiveBytes = registry.MaxAssetBytes

// staleAfter mirrors the plugin installer: a cached index older than this
// is refreshed on the next Status.
var staleAfter = time.Hour

// Typed errors; the admin API maps them to 4xx responses with their text.
var (
	ErrNotFound             = errors.New("this theme is not in the directory")
	ErrNotTheme             = errors.New("this directory entry is not a theme")
	ErrIncompatible         = errors.New("this theme requires a newer goblog")
	ErrChecksum             = errors.New("the downloaded archive does not match the directory index; the index may be stale, refresh and try again")
	ErrAlreadyInstalled     = errors.New("a theme with this name is already installed")
	ErrNotInstalled         = errors.New("this theme is not installed")
	ErrBuiltin              = errors.New("built-in themes cannot be installed, updated or removed")
	ErrActive               = errors.New("the active theme cannot be removed; activate another theme first")
	ErrLoad                 = errors.New("the theme failed to load")
	ErrDirectoryUnavailable = errors.New("the theme directory index is unavailable")
	ErrUpToDate             = errors.New("theme is already at the directory's version")
	ErrDownload             = errors.New("could not download the theme")
	ErrWrite                = errors.New("could not write to the installed themes directory; check that it exists and goblog can write to it")
)

// Installer wires the themes index, the theme roots and the active-theme
// setting together.
type Installer struct {
	Dir         string                  // where installs go (theme.InstalledRoot())
	Directory   *pinstaller.Fetcher     // own instance, pointed at the themes index
	Version     string                  // running goblog version
	Client      *http.Client            // downloads; nil → 60s timeout default
	IndexURL    func() string           // theme_directory_url setting
	ActiveTheme func() string           // the theme currently rendering
	Activate    func(name string) error // persist the setting and hot-reload

	mu      sync.Mutex // serializes Install/Update/Uninstall/ActivateTheme
	urlMu   sync.Mutex
	lastURL string
}

// Installed is a theme goblog can activate.
type Installed struct {
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Version         string `json:"version,omitempty"`
	Builtin         bool   `json:"builtin"`
	Active          bool   `json:"active"`
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version,omitempty"`
	// ScreenshotURL is the directory's screenshot for the theme, when the
	// index lists it; built-in and hand-installed themes have none.
	ScreenshotURL string `json:"screenshot_url,omitempty"`
}

// Available is a directory theme that is not installed.
type Available struct {
	directory.Entry
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// Status is everything the admin page needs in one call.
type Status struct {
	Installed      []Installed `json:"installed"`
	Available      []Available `json:"available"`
	Active         string      `json:"active"`
	DirectoryURL   string      `json:"directory_url"`
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

// manifestFile is written next to an installed theme so Status can report
// its version without the index.
const manifestFile = "goblog-theme.json"

// installedManifest is what an install writes next to the theme: the index
// entry's manifest fields plus the installed version, so Status can report
// it without the index.
type installedManifest struct {
	registry.ThemeManifest
	Version string `json:"version"`
}

func allowedScheme(u *url.URL) bool {
	host := u.Hostname()
	return u.Scheme == "https" || (u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1"))
}

func (i *Installer) client() *http.Client {
	base := i.Client
	if base == nil {
		base = &http.Client{Timeout: 60 * time.Second}
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
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

// ensureIndex mirrors the plugin installer: refetch on a URL change or a
// stale cache, otherwise defer to the fetcher's own throttled first fetch.
func (i *Installer) ensureIndex(indexURL string) {
	i.urlMu.Lock()
	changed := indexURL != i.lastURL
	i.lastURL = indexURL
	i.urlMu.Unlock()
	if changed {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Theme installer: fetching directory index at %s: %v", indexURL, err)
		}
		return
	}
	if fetchedAt := i.Directory.FetchedAt(); !fetchedAt.IsZero() && time.Since(fetchedAt) > staleAfter {
		if err := i.Directory.Refresh(indexURL); err != nil {
			log.Printf("Theme installer: refreshing stale directory index at %s: %v", indexURL, err)
		}
		return
	}
	i.Directory.Ensure(indexURL)
}

// installedVersion reads the manifest an install wrote ("" for built-ins
// or hand-copied themes).
func installedVersion(dir string) (installedManifest, bool) {
	b, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return installedManifest{}, false
	}
	var m installedManifest
	if json.Unmarshal(b, &m) != nil {
		return installedManifest{}, false
	}
	return m, true
}

// Status lists every theme on disk (with update info for installed ones)
// and the directory themes that are not installed.
func (i *Installer) Status() Status {
	indexURL := i.IndexURL()
	i.ensureIndex(indexURL)
	_, entries, ok := i.Directory.Index()
	st := Status{Active: i.ActiveTheme(), DirectoryURL: indexURL, Installed: []Installed{}, Available: []Available{}}
	if !ok {
		st.IndexError = "could not fetch the theme directory index"
	} else {
		st.IndexFetchedAt = i.Directory.FetchedAt().UTC().Format(time.RFC3339)
	}
	st.DirWritable, st.DirError = i.probeDirWritable()

	byName := map[string]directory.Entry{}
	for _, e := range entries {
		if e.Kind != registry.KindTheme || !directory.ValidName(e.Name) {
			continue
		}
		e.SourceURL = safeURL(e.SourceURL)
		e.DownloadURL = safeURL(e.DownloadURL)
		e.ScreenshotURL = safeURL(e.ScreenshotURL)
		byName[e.Name] = e
	}
	onDisk := map[string]bool{}
	for _, name := range theme.List() {
		onDisk[name] = true
		row := Installed{Name: name, DisplayName: name, Builtin: theme.IsBuiltin(name), Active: name == st.Active}
		if dir, ok := theme.Dir(name); ok {
			if m, ok := installedVersion(dir); ok {
				row.DisplayName, row.Version = m.DisplayName, m.Version
			}
		}
		if e, ok := byName[name]; ok && !row.Builtin {
			row.LatestVersion = e.Version
			row.UpdateAvailable = row.Version != "" && pinstaller.Newer(e.Version, row.Version)
			row.ScreenshotURL = e.ScreenshotURL
		}
		st.Installed = append(st.Installed, row)
	}
	for _, e := range byName {
		if onDisk[e.Name] {
			continue
		}
		a := Available{Entry: e, Compatible: true}
		switch {
		case e.InstallType != "theme":
			a.Compatible, a.Reason = false, "not installable from the directory"
		case !pinstaller.Compatible(i.Version, e.MinGoblogVersion):
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

// lookup finds a theme entry and applies the checks that need no download.
func (i *Installer) lookup(name string) (directory.Entry, error) {
	if !directory.ValidName(name) || !theme.ValidName(name) {
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
	if e.Kind != registry.KindTheme || e.InstallType != "theme" {
		return directory.Entry{}, ErrNotTheme
	}
	if !directory.ValidName(e.Name) || !theme.ValidName(e.Name) {
		return directory.Entry{}, fmt.Errorf("%w: invalid theme name %q", ErrLoad, e.Name)
	}
	if !pinstaller.Compatible(i.Version, e.MinGoblogVersion) {
		return directory.Entry{}, fmt.Errorf("%w (%s; this is %s)", ErrIncompatible, e.MinGoblogVersion, i.Version)
	}
	return e, nil
}

// fetchAndCheck downloads the archive, verifies its content hash against
// the index and parses its templates. Nothing touches the disk here.
func (i *Installer) fetchAndCheck(ctx context.Context, e directory.Entry) (map[string][]byte, error) {
	zb, err := i.download(ctx, e.DownloadURL)
	if err != nil {
		return nil, err
	}
	files, err := registry.ParseArchive(zb)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	if registry.ContentHash(files) != e.SHA256 {
		return nil, ErrChecksum
	}
	if err := theme.ValidateFiles(files); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	return files, nil
}

// Install downloads, verifies and unpacks a directory theme.
func (i *Installer) Install(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return Result{}, ErrBuiltin
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	if _, ok := theme.Dir(e.Name); ok {
		return Result{}, ErrAlreadyInstalled
	}
	files, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	if err := i.place(e, files); err != nil {
		return Result{}, err
	}
	log.Printf("Installed theme %s v%s from %s", e.Name, e.Version, e.DownloadURL)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Installed %s v%s", e.DisplayName, e.Version)}, nil
}

// Update replaces an installed theme with the index version. The previous
// directory is kept aside until the new one is in place and restored if
// anything fails; the active theme is reloaded afterwards.
func (i *Installer) Update(ctx context.Context, name string) (Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return Result{}, ErrBuiltin
	}
	dir, ok := theme.Dir(name)
	if !ok {
		return Result{}, ErrNotInstalled
	}
	e, err := i.lookup(name)
	if err != nil {
		return Result{}, err
	}
	current, _ := installedVersion(dir)
	if current.Version != "" && !pinstaller.Newer(e.Version, current.Version) {
		return Result{}, fmt.Errorf("%w: %s is at v%s; the directory has v%s", ErrUpToDate, name, current.Version, e.Version)
	}
	files, err := i.fetchAndCheck(ctx, e)
	if err != nil {
		return Result{}, err
	}
	if err := i.place(e, files); err != nil {
		return Result{}, err
	}
	if i.ActiveTheme() == e.Name && i.Activate != nil {
		if err := i.Activate(e.Name); err != nil {
			log.Printf("Theme installer: reloading %s after update: %v", e.Name, err)
		}
	}
	log.Printf("Updated theme %s to v%s", e.Name, e.Version)
	return Result{Name: e.Name, Version: e.Version, Message: fmt.Sprintf("Updated %s to v%s", e.DisplayName, e.Version)}, nil
}

// place writes files into a temp dir under Dir and swaps it into
// Dir/<name>, setting an existing directory aside as <name>.prev until the
// swap succeeds. Any failure restores the previous directory.
func (i *Installer) place(e directory.Entry, files map[string][]byte) error {
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	tmp, err := os.MkdirTemp(i.Dir, "."+e.Name+".tmp-")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	defer os.RemoveAll(tmp)
	// MkdirTemp creates the directory 0700; loosen it so the installed
	// theme is readable by other uids (e.g. a web server running as a
	// different user on a bind mount) once it is renamed into place.
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	for p, b := range files {
		full := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
	}
	// Homepage is not in the index; leave it unset rather than guess.
	m := registry.ThemeManifest{Name: e.Name, DisplayName: e.DisplayName, Description: e.Description, Author: e.Author, License: e.License, MinGoblogVersion: e.MinGoblogVersion}
	mb, _ := json.MarshalIndent(installedManifest{m, e.Version}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, manifestFile), mb, 0o644); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}

	final := filepath.Join(i.Dir, e.Name)
	prev := final + ".prev"
	os.RemoveAll(prev)
	hadPrev := false
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, prev); err != nil {
			return fmt.Errorf("%w: %v", ErrWrite, err)
		}
		hadPrev = true
	}
	if err := os.Rename(tmp, final); err != nil {
		if hadPrev {
			// The previous theme was set aside a moment ago; if it cannot be
			// put back the site has no copy of it at all, which the operator
			// must hear about alongside the original failure.
			if rerr := os.Rename(prev, final); rerr != nil {
				return fmt.Errorf("%w: %v (and restoring the previous theme from %s failed: %v)", ErrWrite, err, prev, rerr)
			}
		}
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	if hadPrev {
		os.RemoveAll(prev)
	}
	return nil
}

// Uninstall removes an installed theme; built-ins and the active theme
// are refused.
func (i *Installer) Uninstall(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if theme.IsBuiltin(name) {
		return ErrBuiltin
	}
	dir, ok := theme.Dir(name)
	if !ok {
		return ErrNotInstalled
	}
	if i.ActiveTheme() == name {
		return ErrActive
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	log.Printf("Uninstalled theme %s", name)
	return nil
}

// ActivateTheme makes name the site's theme (built-in or installed).
func (i *Installer) ActivateTheme(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := theme.Dir(name); !ok && name != theme.DefaultName {
		return ErrNotInstalled
	}
	if i.Activate == nil {
		return errors.New("theme activation is not wired")
	}
	return i.Activate(name)
}

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
	req.Header.Set("User-Agent", "goblog-theme-installer/"+i.Version)
	resp, err := i.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned HTTP %d", ErrDownload, rawURL, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxArchiveBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if len(b) > maxArchiveBytes {
		return nil, fmt.Errorf("%w: archive is larger than %d bytes", ErrLoad, maxArchiveBytes)
	}
	return b, nil
}

func (i *Installer) probeDirWritable() (bool, string) {
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return false, err.Error()
	}
	f, err := os.CreateTemp(i.Dir, ".probe-*")
	if err != nil {
		return false, err.Error()
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return false, closeErr.Error()
	}
	if removeErr != nil {
		return false, removeErr.Error()
	}
	return true, ""
}

func safeURL(u string) string {
	trimmed := strings.TrimSpace(u)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return trimmed
	}
	return ""
}
