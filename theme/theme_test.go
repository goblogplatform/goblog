package theme

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// roots points BuiltinRoot and the installed root at fresh temp dirs and
// creates a "default" theme in the built-in root.
func roots(t *testing.T) (builtin, installed string) {
	t.Helper()
	builtin, installed = t.TempDir(), t.TempDir()
	old := BuiltinRoot
	BuiltinRoot = builtin
	t.Cleanup(func() { BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "default home"})
	return builtin, installed
}

// mkTheme writes templates/<name> files under dir.
func mkTheme(t *testing.T, dir string, templates map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range templates {
		if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstalledRoot(t *testing.T) {
	t.Setenv("THEMES_INSTALLED_DIR", "")
	if got := InstalledRoot(); got != filepath.Join(BuiltinRoot, "installed") {
		t.Errorf("default installed root = %q", got)
	}
	t.Setenv("THEMES_INSTALLED_DIR", "/srv/themes")
	if got := InstalledRoot(); got != "/srv/themes" {
		t.Errorf("env installed root = %q", got)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"default": true, "my-Theme_2": true, "": false, "../x": false, "a b": false, "installed": false, "shared": false} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDirAndList(t *testing.T) {
	builtin, installed := roots(t)
	mkTheme(t, filepath.Join(builtin, "minimal"), map[string]string{"home.html": "f"})
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "o"})
	mkTheme(t, filepath.Join(installed, "minimal"), map[string]string{"home.html": "shadowed"})
	os.MkdirAll(filepath.Join(builtin, "notatheme"), 0o755)                // no templates/ → ignored
	os.MkdirAll(filepath.Join(installed, "installed", "templates"), 0o755) // reserved name → ignored

	if dir, ok := Dir("minimal"); !ok || dir != filepath.Join(builtin, "minimal") {
		t.Errorf("built-in wins: %q %v", dir, ok)
	}
	if dir, ok := Dir("ocean"); !ok || dir != filepath.Join(installed, "ocean") {
		t.Errorf("installed resolves: %q %v", dir, ok)
	}
	for _, bad := range []string{"nope", "notatheme", "../default", "installed"} {
		if _, ok := Dir(bad); ok {
			t.Errorf("Dir(%q) should not resolve", bad)
		}
	}
	if !IsBuiltin("minimal") || IsBuiltin("ocean") || IsBuiltin("nope") {
		t.Error("IsBuiltin wrong")
	}
	if got := List(); !reflect.DeepEqual(got, []string{"default", "minimal", "ocean"}) {
		t.Errorf("List = %v", got)
	}
}

func TestListWithoutRoots(t *testing.T) {
	old := BuiltinRoot
	BuiltinRoot = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", filepath.Join(t.TempDir(), "missing"))
	if got := List(); !reflect.DeepEqual(got, []string{"default"}) {
		t.Errorf("List with no roots = %v", got)
	}
}

// TestScreenshot: a theme's screenshot is screenshot.png (or .jpg) beside
// its templates/, in either root; a theme without one, or a name that is
// not a theme, has none.
func TestScreenshot(t *testing.T) {
	builtin, installed := t.TempDir(), t.TempDir()
	old := BuiltinRoot
	BuiltinRoot = builtin
	t.Cleanup(func() { BuiltinRoot = old })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	for _, name := range []string{"default", "bare"} {
		os.MkdirAll(filepath.Join(builtin, name, "templates"), 0o755)
	}
	os.WriteFile(filepath.Join(builtin, "default", "screenshot.png"), []byte("png"), 0o644)
	os.MkdirAll(filepath.Join(installed, "ocean", "templates"), 0o755)
	os.WriteFile(filepath.Join(installed, "ocean", "screenshot.jpg"), []byte("jpg"), 0o644)
	os.WriteFile(filepath.Join(builtin, "screenshot.png"), []byte("not a theme"), 0o644)

	if p, ok := Screenshot("default"); !ok || p != filepath.Join(builtin, "default", "screenshot.png") {
		t.Errorf("default: %q %v", p, ok)
	}
	if p, ok := Screenshot("ocean"); !ok || p != filepath.Join(installed, "ocean", "screenshot.jpg") {
		t.Errorf("ocean: %q %v", p, ok)
	}
	for _, name := range []string{"bare", "missing", "../default", ""} {
		if p, ok := Screenshot(name); ok {
			t.Errorf("%q: unexpected screenshot %q", name, p)
		}
	}
}
