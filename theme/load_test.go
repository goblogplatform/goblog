package theme

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func withShared(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := SharedDir
	SharedDir = dir
	t.Cleanup(func() { SharedDir = old })
	os.WriteFile(filepath.Join(dir, "shared.html"), []byte(`{{ define "shared" }}SHARED{{ end }}`), 0o644)
}

func render(t *testing.T, tmpl *template.Template, name string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, nil); err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return buf.String()
}

func TestLoad_OverridesByName(t *testing.T) {
	builtin, installed := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "default home", "post.html": `default post {{ template "shared" }}`})
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "ocean home"})

	tmpl, loaded, err := Load("ocean", nil)
	if err != nil || loaded != "ocean" {
		t.Fatalf("Load: %q %v", loaded, err)
	}
	if got := render(t, tmpl, "home.html"); got != "ocean home" {
		t.Errorf("theme template should win: %q", got)
	}
	if got := render(t, tmpl, "post.html"); got != "default post SHARED" {
		t.Errorf("missing templates fall back to default (with shared): %q", got)
	}
}

func TestLoad_FallsBackToDefault(t *testing.T) {
	builtin, installed := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(installed, "broken"), map[string]string{"home.html": "{{ if }}"})
	_ = builtin
	for _, name := range []string{"broken", "missing", "../x"} {
		tmpl, loaded, err := Load(name, nil)
		if err != nil || loaded != DefaultName {
			t.Errorf("Load(%q) = %q, %v; want default", name, loaded, err)
		}
		if got := render(t, tmpl, "home.html"); got != "default home" {
			t.Errorf("Load(%q) rendered %q", name, got)
		}
	}
}

func TestLoad_FuncMapAndBrokenDefault(t *testing.T) {
	builtin, _ := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": `{{ shout "hi" }}`})
	tmpl, _, err := Load(DefaultName, template.FuncMap{"shout": strings.ToUpper})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, tmpl, "home.html"); got != "HI" {
		t.Errorf("funcMap not applied: %q", got)
	}
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "{{ if }}"})
	if _, _, err := Load(DefaultName, nil); err == nil {
		t.Error("a broken default theme must be an error, not a silent fallback")
	}
}

func TestValidateFiles(t *testing.T) {
	roots(t)
	withShared(t)
	ok := map[string][]byte{"templates/home.html": []byte(`{{ template "shared" }} ok`), "static/a.css": []byte("x"), "templates/sub/ignored.html": []byte("{{ if }}")}
	if err := ValidateFiles(ok); err != nil {
		t.Errorf("valid theme rejected: %v", err)
	}
	bad := map[string][]byte{"templates/home.html": []byte("{{ if }}")}
	if err := ValidateFiles(bad); err == nil || !strings.Contains(err.Error(), "templates/home.html") {
		t.Errorf("want the broken file named, got %v", err)
	}
	if err := ValidateFiles(map[string][]byte{"static/only.css": []byte("x")}); err == nil {
		t.Error("a theme with no templates is not a theme")
	}
}

func TestStaticHandler_FallsBackToDefault(t *testing.T) {
	builtin, installed := roots(t)
	os.MkdirAll(filepath.Join(builtin, DefaultName, "static", "css"), 0o755)
	os.WriteFile(filepath.Join(builtin, DefaultName, "static", "css", "base.css"), []byte("base"), 0o644)
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "o"})
	os.MkdirAll(filepath.Join(installed, "ocean", "static", "css"), 0o755)
	os.WriteFile(filepath.Join(installed, "ocean", "static", "css", "theme.css"), []byte("ocean"), 0o644)
	os.WriteFile(filepath.Join(builtin, "secret.txt"), []byte("no"), 0o644)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/theme/*filepath", StaticHandler(func() string { return "ocean" }))
	get := func(p string) (int, string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w.Code, w.Body.String()
	}
	if code, body := get("/theme/css/theme.css"); code != 200 || body != "ocean" {
		t.Errorf("theme file: %d %q", code, body)
	}
	if code, body := get("/theme/css/base.css"); code != 200 || body != "base" {
		t.Errorf("default fallback: %d %q", code, body)
	}
	if code, _ := get("/theme/css/nope.css"); code != 404 {
		t.Errorf("missing: %d", code)
	}
	if code, _ := get("/theme/../../secret.txt"); code == 200 {
		t.Error("traversal must not escape the static dir")
	}
}
