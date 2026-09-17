package admin_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"goblog/plugin/installer"
	"goblog/plugins/directory"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// pluginsHarness wires a router with the plugin API against a fixture index.
type pluginsHarness struct {
	router *gin.Engine
	auth   *Auth
	inst   *installer.Installer
	srv    *httptest.Server
	ad     admin.Admin
}

func newPluginsHarness(t *testing.T) *pluginsHarness {
	t.Helper()
	src, err := os.ReadFile("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256hex(src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason","license":"MIT","source_url":"https://github.com/x/hello","download_url":"http://127.0.0.1/hello.go","sha256":"` + sha + `","min_goblog_version":"0.2.6","install_type":"dynamic","released_at":"2026-09-15T00:00:00Z","detail_url":"","stars":7},
			{"name":"zeta","display_name":"Zeta","description":"Other thing","version":"0.1.0","author":"Someone","license":"MIT","source_url":"https://github.com/x/zeta","download_url":"https://example.test/z.go","sha256":"00","min_goblog_version":"0.1.0","install_type":"dynamic","released_at":"2026-01-01T00:00:00Z","detail_url":"","stars":1}]`))
		case "/hello.go":
			w.Write(src)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	reg := plugin.NewRegistry(db)
	reg.Init()
	inst := &installer.Installer{
		Dir: t.TempDir(), Registry: reg, Directory: directory.NewFetcher(srv.Client()),
		Version: "v0.2.7", Client: rewritingClient(srv), Enabled: true,
		IndexURL: func() string { return srv.URL + "/index.json" },
	}
	ad.Installer = inst

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	router.GET("/api/v1/plugins/status", ad.PluginStatus)
	router.GET("/api/v1/plugins/directory", ad.PluginDirectory)
	router.POST("/api/v1/plugins/install", ad.InstallPlugin)
	router.POST("/api/v1/plugins/update", ad.UpdatePlugin)
	router.DELETE("/api/v1/plugins/:name", ad.UninstallPlugin)
	router.POST("/api/v1/plugins/refresh", ad.RefreshPluginDirectory)
	return &pluginsHarness{router: router, auth: a, inst: inst, srv: srv, ad: ad}
}

func (h *pluginsHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func TestPluginAPI_NonAdmin(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/v1/plugins/status"}, {"GET", "/api/v1/plugins/directory"},
		{"POST", "/api/v1/plugins/install"}, {"POST", "/api/v1/plugins/update"},
		{"DELETE", "/api/v1/plugins/hello"}, {"POST", "/api/v1/plugins/refresh"},
	} {
		if w := h.do(c.m, c.p, `{"name":"hello"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", c.m, c.p, w.Code)
		}
	}
}

func TestPluginAPI_StatusDirectoryInstallUninstall(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	w := h.do("GET", "/api/v1/plugins/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var st installer.Status
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.DynamicEnabled || len(st.Available) != 2 || st.Available[0].Name != "hello" {
		t.Errorf("status = %+v", st)
	}

	// Directory search + sort.
	w = h.do("GET", "/api/v1/plugins/directory?q=other", "")
	var avail []installer.Available
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 1 || avail[0].Name != "zeta" {
		t.Errorf("search 'other' = %+v", avail)
	}
	w = h.do("GET", "/api/v1/plugins/directory?sort=name", "")
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 2 || avail[0].Name != "hello" || avail[1].Name != "zeta" {
		t.Errorf("sort=name = %+v", avail)
	}
	w = h.do("GET", "/api/v1/plugins/directory?sort=newest", "")
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 2 || avail[0].Name != "hello" {
		t.Errorf("sort=newest = %+v", avail)
	}

	// Install.
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Installed Hello v1.0.0") {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already installed") {
		t.Errorf("second install: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", w.Code)
	}
	w = h.do("POST", "/api/v1/plugins/install", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing name: %d", w.Code)
	}
	w = h.do("POST", "/api/v1/plugins/update", `{"name":"hello"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update at latest should be 400, got %d %s", w.Code, w.Body.String())
	}

	// Uninstall.
	w = h.do("DELETE", "/api/v1/plugins/hello", "")
	if w.Code != http.StatusOK {
		t.Errorf("uninstall: %d %s", w.Code, w.Body.String())
	}
	w = h.do("DELETE", "/api/v1/plugins/hello", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("second uninstall: %d", w.Code)
	}

	// Disabled: 4xx with the message, status still fine.
	h.inst.Enabled = false
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"hello"}`)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "ENABLE_DYNAMIC_PLUGINS") {
		t.Errorf("disabled: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/refresh", "")
	if w.Code != http.StatusOK {
		t.Errorf("refresh: %d %s", w.Code, w.Body.String())
	}
}

func TestPluginAPI_NoInstaller(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(true)
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	router := gin.New()
	router.GET("/api/v1/plugins/status", ad.PluginStatus)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/plugins/status", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("without an installer the API should answer 503, got %d", w.Code)
	}
}

// rewritingClient sends requests for the port-less loopback host used in the
// fixture index (http://127.0.0.1/hello.go — plain HTTP is only accepted for
// loopback) to the fixture server's real port.
func rewritingClient(srv *httptest.Server) *http.Client {
	base := srv.Client()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "127.0.0.1" {
			u := *r.URL
			real, _ := http.NewRequest(r.Method, srv.URL+u.Path, nil)
			real.Header = r.Header
			return base.Transport.RoundTrip(real)
		}
		return base.Transport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func adminFromHarness(h *pluginsHarness) *admin.Admin { return &h.ad }

func TestAdminPluginsPage(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	h.router.SetHTMLTemplate(tmpl)
	h.router.GET("/admin/plugins", func(c *gin.Context) { adminFromHarness(h).AdminPlugins(c) })

	w := h.do("GET", "/admin/plugins", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`id="tab-installed"`, `id="tab-browse"`, `/api/v1/plugins/status`, `href="/admin/plugins"`, "ENABLE_DYNAMIC_PLUGINS"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}
