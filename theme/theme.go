// Package theme resolves, lists and loads goblog themes. A theme is a
// directory with templates/ (and optionally static/). Built-in themes ship
// in the image under themes/; themes installed from the directory live in
// a separate, persisted root so a Docker bind mount can keep them across
// redeploys without hiding the built-ins.
package theme

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// BuiltinRoot holds the themes compiled into the image (and, on bare
// metal, whatever the operator drops there). SharedDir holds the templates
// every theme gets. Both are package variables so tests can point them at
// temp dirs.
var (
	BuiltinRoot = "themes"
	SharedDir   = "templates/shared"
)

// DefaultName is the theme every other theme is layered on.
const DefaultName = "default"

// InstalledRoot is where the theme installer writes: $THEMES_INSTALLED_DIR
// or themes/installed. Read on every call so tests can change it.
func InstalledRoot() string {
	if v := os.Getenv("THEMES_INSTALLED_DIR"); v != "" {
		return v
	}
	return filepath.Join(BuiltinRoot, "installed")
}

// namePattern is the on-disk rule, unchanged from the old inline check in
// main: a theme name is a single safe path segment.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// reserved are directory names under a root that are not themes.
var reserved = map[string]bool{"installed": true, "shared": true}

// ValidName reports whether name may be looked up on disk.
func ValidName(name string) bool { return namePattern.MatchString(name) && !reserved[name] }

// Dir returns the directory of a theme — the built-in one first, then the
// installed one — and whether it exists. A directory counts only when it
// has a templates/ subdirectory.
func Dir(name string) (string, bool) {
	if !ValidName(name) {
		return "", false
	}
	for _, root := range []string{BuiltinRoot, InstalledRoot()} {
		dir := filepath.Join(root, name)
		if hasTemplates(dir) {
			return dir, true
		}
	}
	return "", false
}

// Screenshot returns the path of a theme's preview image — screenshot.png
// or screenshot.jpg beside its templates/, the same file the directory
// requires of a published theme — and whether it has one.
func Screenshot(name string) (string, bool) {
	dir, ok := Dir(name)
	if !ok {
		return "", false
	}
	for _, file := range []string{"screenshot.png", "screenshot.jpg"} {
		p := filepath.Join(dir, file)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// IsBuiltin reports whether name resolves to the built-in root. Dir looks
// there first, so a theme is built-in exactly when Dir lands there — even
// if an installed copy of the same name exists underneath.
func IsBuiltin(name string) bool {
	dir, ok := Dir(name)
	return ok && dir == filepath.Join(BuiltinRoot, name)
}

// List returns every theme name from both roots, sorted. "default" is
// always listed: it is the baseline every other theme is layered on, and
// Load requires it (a missing or broken default is a startup error).
func List() []string {
	seen := map[string]bool{DefaultName: true}
	for _, root := range []string{BuiltinRoot, InstalledRoot()} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && ValidName(e.Name()) && hasTemplates(filepath.Join(root, e.Name())) {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func hasTemplates(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "templates"))
	return err == nil && info.IsDir()
}
