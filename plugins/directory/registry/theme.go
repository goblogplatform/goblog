package registry

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds of entry the directory publishes.
const (
	KindPlugin = "plugin"
	KindTheme  = "theme"
)

// MaxArchiveEntries bounds a theme archive; a theme is a few dozen files.
const MaxArchiveEntries = 2000

// MaxScreenshotBytes caps screenshot.png/.jpg. GitHub's contents API only
// returns files up to 1 MiB inline, so a larger screenshot could not be
// checked anyway.
const MaxScreenshotBytes = 1 << 20

// MaxTemplateBytes caps one templates/* entry in a theme archive. registry
// must not import theme (layering), so this is kept equal to
// theme.MaxTemplateBytes by hand; the installer applies that one when it
// re-validates the files it downloads.
const MaxTemplateBytes = 256 << 10

// minGoblogVersion is the floor for a theme's min_goblog_version: the first
// goblog release that can install a directory theme at all.
var minGoblogVersion = [3]int{0, 5, 0}

// ThemeManifest is goblog-theme.json at the root of a theme repository.
type ThemeManifest struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Author           string `json:"author"`
	License          string `json:"license"`
	MinGoblogVersion string `json:"min_goblog_version"`
	Homepage         string `json:"homepage"`
}

// reservedThemeNames are directory names the loader treats specially or
// the base themes every goblog ships; a directory theme may not claim
// them (nor the route names in reservedNames, which apply to both kinds).
// forest is not here on purpose: it is published to the directory and no
// longer ships with goblog, so it is an ordinary directory theme.
var reservedThemeNames = map[string]bool{"default": true, "minimal": true, "installed": true, "shared": true}

// ParseThemeManifest decodes and validates a theme manifest.
func ParseThemeManifest(b []byte) (ThemeManifest, error) {
	var m ThemeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return ThemeManifest{}, fmt.Errorf("goblog-theme.json: %w", err)
	}
	var problems []string
	if !NamePattern.MatchString(m.Name) {
		problems = append(problems, "name must match ^[a-z0-9-]+$")
	} else if reservedThemeNames[m.Name] || reservedNames[m.Name] {
		problems = append(problems, fmt.Sprintf("name %q is reserved", m.Name))
	}
	for _, f := range []struct{ field, v string }{{"display_name", m.DisplayName}, {"description", m.Description}, {"author", m.Author}} {
		if strings.TrimSpace(f.v) == "" {
			problems = append(problems, f.field+" is required")
		}
	}
	if !knownLicenses[m.License] {
		problems = append(problems, fmt.Sprintf("license %q is not a known SPDX identifier", m.License))
	}
	if !versionPattern.MatchString(m.MinGoblogVersion) {
		problems = append(problems, "min_goblog_version must be a plain semver like 0.5.0")
	} else if !atLeast(m.MinGoblogVersion, minGoblogVersion) {
		problems = append(problems, "min_goblog_version must be at least 0.5.0")
	}
	if len(problems) > 0 {
		return ThemeManifest{}, fmt.Errorf("goblog-theme.json: %s", strings.Join(problems, "; "))
	}
	return m, nil
}

// atLeast reports whether v (already known to match versionPattern, so this
// never fails to parse) is >= floor, comparing major, then minor, then patch.
func atLeast(v string, floor [3]int) bool {
	parts := strings.SplitN(v, ".", 3)
	for i, floorPart := range floor {
		n, _ := strconv.Atoi(parts[i])
		if n != floorPart {
			return n > floorPart
		}
	}
	return true
}

// ParseArchive reads a theme archive (GitHub's tag zipball, or any zip with
// the same layout) into memory, keeping only templates/** and static/**.
// GitHub wraps everything in one "<repo>-<tag>/" folder, which is stripped
// when every entry shares it. Symlinks and paths that escape the archive
// are refused; the entry count and total size are bounded.
func ParseArchive(zipBytes []byte) (map[string][]byte, error) {
	if len(zipBytes) > MaxAssetBytes {
		// Defence in depth: the caller (source.go) already bounds a
		// downloaded release asset to MaxAssetBytes, but ParseArchive
		// should not trust that on its own.
		return nil, fmt.Errorf("archive: %d bytes exceeds the %d byte limit", len(zipBytes), MaxAssetBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if len(zr.File) > MaxArchiveEntries {
		return nil, fmt.Errorf("archive has %d entries; the limit is %d", len(zr.File), MaxArchiveEntries)
	}
	prefix := commonFolder(zr.File)
	files := map[string][]byte{}
	total := 0
	for _, f := range zr.File {
		// The zip spec mandates forward slashes; a name carrying a NUL or a
		// backslash is either malformed or an attempt to smuggle a path
		// past fs.ValidPath (NUL also breaks ContentHash's \0-separated
		// encoding, letting two different file sets collide).
		if strings.ContainsAny(f.Name, "\x00\\") {
			return nil, fmt.Errorf("archive: unsafe path %q", f.Name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("archive: %s is a symlink", f.Name)
		}
		if strings.HasSuffix(f.Name, "/") {
			continue // directory entry
		}
		name := strings.TrimPrefix(f.Name, prefix)
		if strings.HasPrefix(f.Name, "/") || !fs.ValidPath(name) {
			return nil, fmt.Errorf("archive: unsafe path %q", f.Name)
		}
		if !strings.HasPrefix(name, "templates/") && !strings.HasPrefix(name, "static/") {
			continue
		}
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("archive: duplicate entry %q", name)
		}
		if f.UncompressedSize64 > uint64(MaxAssetBytes) {
			return nil, fmt.Errorf("archive: %s is larger than %d bytes", f.Name, MaxAssetBytes)
		}
		if strings.HasPrefix(name, "templates/") && f.UncompressedSize64 > uint64(MaxTemplateBytes) {
			return nil, fmt.Errorf("archive: %s is %d bytes; the limit is %d (256 KiB)", f.Name, f.UncompressedSize64, MaxTemplateBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(MaxAssetBytes)+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("archive: %s: %w", f.Name, err)
		}
		total += len(b)
		if total > MaxAssetBytes {
			return nil, fmt.Errorf("archive: extracted size exceeds %d bytes", MaxAssetBytes)
		}
		files[name] = b
	}
	return files, nil
}

// commonFolder returns "<folder>/" when every entry lives under the same
// top-level folder, else "". A theme archive's own content roots are
// templates/ and static/, so a first path segment equal to either of those
// is never treated as a wrapper to strip - the archive is already flat.
// "." and ".." are likewise never stripped as a wrapper: fs.ValidPath then
// refuses them, rather than commonFolder silently consuming them.
func commonFolder(entries []*zip.File) string {
	if len(entries) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(entries[0].Name, "/")
	if !ok || first == "" {
		return ""
	}
	if first == "templates" || first == "static" || first == "." || first == ".." {
		return ""
	}
	prefix := first + "/"
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, prefix) {
			return ""
		}
	}
	return prefix
}

// ContentHash is the index's sha256 for a theme: a hash of the extracted
// files rather than the archive bytes, because GitHub does not promise
// that a tag's zipball is byte-stable. Each file contributes its path, its
// length and its bytes, in path order.
func ContentHash(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		io.WriteString(h, p)
		h.Write([]byte{0})
		io.WriteString(h, strconv.Itoa(len(files[p])))
		h.Write([]byte{0})
		h.Write(files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ThemeValidator checks a theme's files the way goblog would load them. The
// real one (theme.ValidateFiles, wrapped by the directory plugin) parses
// the templates on top of the shared and default sets; tests use
// FakeThemeValidator.
type ThemeValidator interface {
	Validate(ctx context.Context, files map[string][]byte) error
}

// FakeThemeValidator accepts everything unless Err is set.
type FakeThemeValidator struct{ Err error }

func (f *FakeThemeValidator) Validate(context.Context, map[string][]byte) error { return f.Err }

// ValidatedTheme is a theme repository that passed every check.
type ValidatedTheme struct {
	Repo, Owner, Name string
	Manifest          ThemeManifest
	Release           Release
	Version           string
	Releases          []Release
	Readme            []byte            // README.md at Release.Tag, kept so BuildTheme need not fetch it again
	Files             map[string][]byte // templates/** and static/**
	SHA256            string            // ContentHash(Files)
	ScreenshotURL     string
}

// ValidateThemeEntry checks a theme repository end to end: a vX.Y.Z
// release, manifest, README and screenshot at that tag, and an archive
// whose templates load.
func ValidateThemeEntry(ctx context.Context, src Source, tv ThemeValidator, repo string) (*ValidatedTheme, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%q: repo must be owner/name", repo)
	}
	latest, releases, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(latest.Tag, "v")
	at := repo + "@" + latest.Tag

	mb, err := src.File(ctx, owner, name, latest.Tag, "goblog-theme.json")
	if err != nil {
		return nil, fmt.Errorf("%s: goblog-theme.json: %w", at, err)
	}
	manifest, err := ParseThemeManifest(mb)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	readme, err := src.File(ctx, owner, name, latest.Tag, "README.md")
	if err != nil {
		return nil, fmt.Errorf("%s: README.md: %w", at, err)
	}
	shot := ""
	for _, candidate := range []string{"screenshot.png", "screenshot.jpg"} {
		b, err := src.File(ctx, owner, name, latest.Tag, candidate)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %v (it must be at most 1 MiB)", at, candidate, err)
		}
		if len(b) > MaxScreenshotBytes {
			return nil, fmt.Errorf("%s: %s is %d bytes; the limit is %d (1 MiB)", at, candidate, len(b), MaxScreenshotBytes)
		}
		shot = candidate
		break
	}
	if shot == "" {
		return nil, fmt.Errorf("%s: screenshot.png (or screenshot.jpg) is required", at)
	}
	zb, err := src.Zipball(ctx, owner, name, latest.Tag)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	files, err := ParseArchive(zb)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	if err := tv.Validate(ctx, files); err != nil {
		return nil, fmt.Errorf("%s: templates do not load: %w", at, err)
	}
	return &ValidatedTheme{
		Repo: repo, Owner: owner, Name: name, Manifest: manifest, Release: latest, Version: version, Releases: releases,
		Readme: readme, Files: files, SHA256: ContentHash(files), ScreenshotURL: src.FileURL(owner, name, latest.Tag, shot),
	}, nil
}

// BuildTheme validates repo and returns the document the directory
// publishes for it under /themes.
func BuildTheme(ctx context.Context, src Source, tv ThemeValidator, repo, baseURL string) (DetailDoc, error) {
	v, err := ValidateThemeEntry(ctx, src, tv, repo)
	if err != nil {
		return DetailDoc{}, err
	}
	ownerRepo := v.Owner + "/" + v.Name
	entry := IndexEntry{
		Kind:             KindTheme,
		Name:             v.Manifest.Name,
		DisplayName:      v.Manifest.DisplayName,
		Description:      v.Manifest.Description,
		Version:          v.Version,
		Author:           v.Manifest.Author,
		License:          v.Manifest.License,
		SourceURL:        "https://github.com/" + ownerRepo,
		DownloadURL:      fmt.Sprintf("https://github.com/%s/archive/refs/tags/%s.zip", ownerRepo, v.Release.Tag),
		SHA256:           v.SHA256,
		MinGoblogVersion: v.Manifest.MinGoblogVersion,
		InstallType:      "theme",
		AllowedHosts:     []string{},
		ReleasedAt:       v.Release.PublishedAt.UTC().Format(time.RFC3339),
		DetailURL:        strings.TrimSuffix(baseURL, "/") + "/themes/" + v.Manifest.Name + ".json",
		ScreenshotURL:    v.ScreenshotURL,
	}
	if stars, err := src.RepoStars(ctx, v.Owner, v.Name); err != nil {
		log.Printf("%s: stars unavailable, using 0: %v", ownerRepo, err)
	} else {
		entry.Stars = stars
	}
	readme, changelog, releases, err := renderDocs(ctx, src, v.Owner, v.Name, v.Release.Tag, v.Readme, v.Releases)
	if err != nil {
		return DetailDoc{}, err
	}
	return DetailDoc{IndexEntry: entry, ReadmeHTML: readme, ChangelogHTML: changelog, Releases: releases}, nil
}

// DetectKind tells a plugin repository from a theme one by which manifest
// its latest release carries, so submitters need not say. Exactly one of
// goblog-plugin.json / goblog-theme.json must be present at the tag.
func DetectKind(ctx context.Context, src Source, repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("%q: repo must be owner/name", repo)
	}
	latest, _, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return "", err
	}
	has := func(path string) (bool, error) {
		_, err := src.File(ctx, owner, name, latest.Tag, path)
		if err == nil {
			return true, nil
		}
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	plugin, err := has("goblog-plugin.json")
	if err != nil {
		return "", err
	}
	theme, err := has("goblog-theme.json")
	if err != nil {
		return "", err
	}
	switch {
	case plugin && theme:
		return "", fmt.Errorf("%s@%s: has both goblog-plugin.json and goblog-theme.json; a repository is one or the other", repo, latest.Tag)
	case plugin:
		return KindPlugin, nil
	case theme:
		return KindTheme, nil
	}
	return "", fmt.Errorf("%s@%s: no goblog-plugin.json or goblog-theme.json at the root; see the publishing docs", repo, latest.Tag)
}
