package theme

import (
	"bytes"
	"html/template"
	"log"
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

func TestLoad_PartialFailureFallsBackWholesale(t *testing.T) {
	builtin, installed := roots(t)
	withShared(t)
	mkTheme(t, filepath.Join(builtin, DefaultName), map[string]string{"home.html": "default home", "post.html": "default post"})
	// home.html parses; post.html does not. ParseFiles has already added
	// home.html to the set by the time it fails, so Load must rebuild the
	// base rather than hand back a half-applied theme.
	mkTheme(t, filepath.Join(installed, "ocean"), map[string]string{"home.html": "ocean home", "post.html": "{{ if }}"})

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	tmpl, loaded, err := Load("ocean", nil)
	if err != nil || loaded != DefaultName {
		t.Fatalf("Load = %q, %v; want default", loaded, err)
	}
	if got := render(t, tmpl, "home.html"); got != "default home" {
		t.Errorf("the theme's good file must not survive its bad one: %q", got)
	}
	if got := render(t, tmpl, "post.html"); got != "default post" {
		t.Errorf("post.html = %q", got)
	}
	if out := logs.String(); !strings.Contains(out, `theme "ocean"`) || !strings.Contains(out, "post.html") || !strings.Contains(out, "falling back to default") {
		t.Errorf("fallback must be logged with the theme and file named, got %q", out)
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

// TestValidateFiles_UndefinedTemplateReference: {{ template "x" }} names
// are only resolved at render time, so the check walks the parsed trees. A
// name the theme does not ship is fine when shared or default provide it
// (the theme's files are layered on top of them), and a {{ define }} in
// any of the theme's own files counts; a name that exists nowhere fails,
// naming the file, even nested in a branch or inside a define block.
func TestValidateFiles_UndefinedTemplateReference(t *testing.T) {
	roots(t)
	withShared(t)
	ok := map[string][]byte{
		"templates/post.html":  []byte(`{{ template "shared" }}{{ template "home.html" . }}{{ template "card" . }}`),
		"templates/parts.html": []byte(`{{ define "card" }}card{{ end }}`),
	}
	if err := ValidateFiles(ok); err != nil {
		t.Errorf("references to shared, default and the theme's own define must pass: %v", err)
	}
	cases := map[string]map[string][]byte{
		"nested in a branch": {"templates/home.html": []byte(`{{ if .X }}{{ range .L }}{{ template "nope" . }}{{ end }}{{ end }}`)},
		"in an else branch":  {"templates/home.html": []byte(`{{ with .X }}x{{ else }}{{ template "nope" . }}{{ end }}`)},
		"inside a define":    {"templates/home.html": []byte(`{{ define "card" }}{{ template "nope" . }}{{ end }}`)},
	}
	for name, files := range cases {
		err := ValidateFiles(files)
		if err == nil || !strings.Contains(err.Error(), "templates/home.html") || !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("%s: want the file and the missing name, got %v", name, err)
		}
	}
}

func TestValidateFiles_SizeCap(t *testing.T) {
	roots(t)
	withShared(t)

	tooBig := bytes.Repeat([]byte("a"), MaxTemplateBytes+1)
	err := ValidateFiles(map[string][]byte{"templates/home.html": tooBig})
	if err == nil || !strings.Contains(err.Error(), "templates/home.html") {
		t.Errorf("want the oversized file named, got %v", err)
	}

	// Exactly at the cap, and valid template text, must still pass.
	atCap := bytes.Repeat([]byte(" "), MaxTemplateBytes)
	if err := ValidateFiles(map[string][]byte{"templates/home.html": atCap}); err != nil {
		t.Errorf("a file exactly at the cap should pass: %v", err)
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
	// Registered for HEAD as well, the handler answers it: a monitor or a
	// proxy checking an asset must not get a 404.
	r.HEAD("/theme/*filepath", StaticHandler(func() string { return "ocean" }))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/theme/css/theme.css", nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", w.Code, w.Body.String())
	}
}
