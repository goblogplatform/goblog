package registry

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
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
// them. forest is not here on purpose: it is compiled in today and is also
// published to the directory, and the installer refuses to install over a
// built-in anyway.
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
	} else if reservedThemeNames[m.Name] {
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
	}
	if len(problems) > 0 {
		return ThemeManifest{}, fmt.Errorf("goblog-theme.json: %s", strings.Join(problems, "; "))
	}
	return m, nil
}

// ParseArchive reads a theme archive (GitHub's tag zipball, or any zip with
// the same layout) into memory, keeping only templates/** and static/**.
// GitHub wraps everything in one "<repo>-<tag>/" folder, which is stripped
// when every entry shares it. Symlinks and paths that escape the archive
// are refused; the entry count and total size are bounded.
func ParseArchive(zipBytes []byte) (map[string][]byte, error) {
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
		if f.UncompressedSize64 > uint64(MaxAssetBytes) {
			return nil, fmt.Errorf("archive: %s is larger than %d bytes", f.Name, MaxAssetBytes)
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
// top-level folder, else "".
func commonFolder(entries []*zip.File) string {
	if len(entries) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(entries[0].Name, "/")
	if !ok || first == "" {
		return ""
	}
	// A theme archive's own content roots are templates/ and static/. If
	// the first entry's top-level segment is one of those, the archive is
	// already flat (no GitHub wrapper folder) - treating it as a wrapper
	// to strip would delete the templates/static prefix itself.
	if first == "templates" || first == "static" {
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
