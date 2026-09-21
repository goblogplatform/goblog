package admin_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	pinstaller "goblog/plugin/installer"
	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"
	"goblog/theme"
	tinstaller "goblog/theme/installer"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func themeZipOf(t *testing.T, prefix string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

// themesHarness wires a router with the theme API against a fixture index:
// one valid theme "ocean" (installable) and one incompatible theme "future",
// with the theme roots pointed at temp dirs (mirrors theme/installer's
// newHarness).
type themesHarness struct {
	router      *gin.Engine
	auth        *Auth
	inst        *tinstaller.Installer
	srv         *httptest.Server
	ad          *admin.Admin
	db          *gorm.DB
	active      string
	activations []string
	index       []directory.Entry
}

func newThemesHarness(t *testing.T) *themesHarness {
	t.Helper()
	builtin, installed := t.TempDir(), t.TempDir()
	oldBuiltin := theme.BuiltinRoot
	theme.BuiltinRoot = builtin
	t.Cleanup(func() { theme.BuiltinRoot = oldBuiltin })
	t.Setenv("THEMES_INSTALLED_DIR", installed)
	shared := t.TempDir()
	oldShared := theme.SharedDir
	theme.SharedDir = shared
	t.Cleanup(func() { theme.SharedDir = oldShared })
	os.WriteFile(filepath.Join(shared, "shared.html"), []byte(`{{ define "shared" }}S{{ end }}`), 0o644)
	os.MkdirAll(filepath.Join(builtin, "default", "templates"), 0o755)
	os.WriteFile(filepath.Join(builtin, "default", "templates", "home.html"), []byte("default"), 0o644)

	h := &themesHarness{active: "default"}
	oceanFiles := map[string][]byte{"templates/home.html": []byte("ocean"), "static/css/o.css": []byte("c")}
	zips := map[string][]byte{
		"/o/ocean/archive/refs/tags/v1.0.0.zip": themeZipOf(t, "ocean-1.0.0/", map[string]string{"templates/home.html": "ocean", "static/css/o.css": "c"}),
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/themes/index.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(h.index)
			return
		}
		if z, ok := zips[r.URL.Path]; ok {
			w.Write(z)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.srv.Close)

	entry := func(name, version string, files map[string][]byte, min string) directory.Entry {
		return directory.Entry{Kind: registry.KindTheme, Name: name, DisplayName: strings.ToUpper(name), Description: "d " + name, Version: version, Author: "a",
			License: "MIT", SourceURL: "https://github.com/o/" + name, DownloadURL: h.srv.URL + "/o/" + name + "/archive/refs/tags/v" + version + ".zip",
			SHA256: registry.ContentHash(files), MinGoblogVersion: min, InstallType: "theme", AllowedHosts: []string{}, Stars: 1,
			ScreenshotURL: "https://raw.test/o/" + name + "/screenshot.png"}
	}
	h.index = []directory.Entry{
		entry("ocean", "1.0.0", oceanFiles, "0.5.0"),
		entry("future", "1.0.0", oceanFiles, "9.0.0"),
	}

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	adp := &ad

	h.inst = &tinstaller.Installer{
		Dir:         installed,
		Directory:   pinstaller.NewFetcher(h.srv.Client()),
		Version:     "v0.5.0",
		Client:      h.srv.Client(),
		IndexURL:    func() string { return h.srv.URL + "/themes/index.json" },
		ActiveTheme: func() string { return h.active },
		Activate: func(name string) error {
			h.activations = append(h.activations, name)
			h.active = name
			return nil
		},
	}
	adp.Themes = h.inst

	router := gin.New()
	router.GET("/api/v1/themes/status", adp.ThemeStatus)
	router.GET("/api/v1/themes/directory", adp.ThemeDirectory)
	router.POST("/api/v1/themes/install", adp.InstallTheme)
	router.POST("/api/v1/themes/update", adp.UpdateTheme)
	router.POST("/api/v1/themes/activate", adp.ActivateTheme)
	router.DELETE("/api/v1/themes/:name", adp.UninstallTheme)
	router.POST("/api/v1/themes/refresh", adp.RefreshThemeDirectory)

	h.router, h.auth, h.ad, h.db = router, a, adp, db
	return h
}

func (h *themesHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func TestThemeAPI_NonAdmin(t *testing.T) {
	h := newThemesHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/v1/themes/status"}, {"GET", "/api/v1/themes/directory"},
		{"POST", "/api/v1/themes/install"}, {"POST", "/api/v1/themes/update"},
		{"POST", "/api/v1/themes/activate"}, {"DELETE", "/api/v1/themes/ocean"},
		{"POST", "/api/v1/themes/refresh"},
	} {
		if w := h.do(c.m, c.p, `{"name":"ocean"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", c.m, c.p, w.Code)
		}
	}
}

func TestThemeAPI_NoInstaller(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(true)
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	router := gin.New()
	router.GET("/api/v1/themes/status", ad.ThemeStatus)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/themes/status", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("without an installer the API should answer 503, got %d", w.Code)
	}
}

func TestThemeAPI_Lifecycle(t *testing.T) {
	h := newThemesHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)

	w := h.do("GET", "/api/v1/themes/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var st tinstaller.Status
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Active != "default" {
		t.Errorf("expected default active, got %+v", st)
	}
	oceanAvailable := false
	for _, a := range st.Available {
		if a.Name == "ocean" {
			oceanAvailable = true
		}
	}
	if !oceanAvailable {
		t.Errorf("expected ocean available, got %+v", st.Available)
	}

	// Install.
	w = h.do("POST", "/api/v1/themes/install", `{"name":"ocean"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Installed OCEAN v1.0.0") {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/api/v1/themes/status", "")
	json.Unmarshal(w.Body.Bytes(), &st)
	oceanInstalled := false
	for _, i := range st.Installed {
		if i.Name == "ocean" {
			oceanInstalled = true
			// The Installed tab renders cards with the directory screenshot.
			if i.ScreenshotURL != "https://raw.test/o/ocean/screenshot.png" {
				t.Errorf("installed ocean screenshot_url = %q", i.ScreenshotURL)
			}
		}
	}
	if !oceanInstalled {
		t.Errorf("expected ocean installed, got %+v", st.Installed)
	}
	if !strings.Contains(w.Body.String(), `"screenshot_url":"https://raw.test/o/ocean/screenshot.png"`) {
		t.Errorf("status JSON must expose screenshot_url for installed themes: %s", w.Body.String())
	}

	// Activate.
	w = h.do("POST", "/api/v1/themes/activate", `{"name":"ocean"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", w.Code, w.Body.String())
	}
	if len(h.activations) == 0 || h.activations[len(h.activations)-1] != "ocean" {
		t.Errorf("expected activation recorder to see ocean, got %v", h.activations)
	}

	// Deleting the active theme is refused.
	w = h.do("DELETE", "/api/v1/themes/ocean", "")
	if w.Code != http.StatusConflict {
		t.Errorf("delete active: expected 409, got %d %s", w.Code, w.Body.String())
	}

	// Activate default, then the delete succeeds.
	w = h.do("POST", "/api/v1/themes/activate", `{"name":"default"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("activate default: %d %s", w.Code, w.Body.String())
	}
	w = h.do("DELETE", "/api/v1/themes/ocean", "")
	if w.Code != http.StatusOK {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}

	// Directory search.
	w = h.do("GET", "/api/v1/themes/directory?q=oce", "")
	var avail []tinstaller.Available
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 1 || avail[0].Name != "ocean" {
		t.Errorf("search 'oce' = %+v", avail)
	}

	// Refresh.
	w = h.do("POST", "/api/v1/themes/refresh", "")
	if w.Code != http.StatusOK {
		t.Errorf("refresh: %d %s", w.Code, w.Body.String())
	}

	// Unknown theme.
	w = h.do("POST", "/api/v1/themes/install", `{"name":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", w.Code)
	}

	// Incompatible theme.
	w = h.do("POST", "/api/v1/themes/install", `{"name":"future"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("incompatible: expected 422, got %d %s", w.Code, w.Body.String())
	}
}

func TestAdminThemesPage(t *testing.T) {
	h := newThemesHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	h.router.SetHTMLTemplate(tmpl)
	h.router.GET("/admin/themes", h.ad.AdminThemes)

	w := h.do("GET", "/admin/themes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`id="tab-installed"`, `id="tab-browse"`, `/api/v1/themes/status`, `href="/admin/themes"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Directory data (theme names, URLs) must never be wired into the page
	// as inline JS via onclick — only via data-* attributes read by a
	// delegated listener, which the browser cannot mis-evaluate.
	if strings.Contains(body, `onclick="themeAction`) {
		t.Errorf("page must not build inline onclick handlers from theme data")
	}
	if !strings.Contains(body, "data-action=") {
		t.Errorf("page missing data-action= driven controls")
	}
	// Installed themes render as a card grid like Browse (#596), inside the
	// opaque admin panel so the page reads on any theme's backdrop.
	for _, want := range []string{`id="installed-cards"`, `class="row g-3"`, `class="admin-panel"`, `screenshot_url`, `theme-placeholder`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `id="installed-rows"`) {
		t.Errorf("installed themes must no longer render as a table")
	}
}
