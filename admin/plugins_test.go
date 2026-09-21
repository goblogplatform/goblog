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
	"regexp"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"goblog/plugin/installer"

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
	ad     *admin.Admin
	db     *gorm.DB
}

func newPluginsHarness(t *testing.T) *pluginsHarness {
	t.Helper()
	src, err := os.ReadFile("../plugin/wasm/testdata/echo.wasm")
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256hex(src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"echo","display_name":"Echo","description":"Says hi","version":"1.2.3","author":"Jason","license":"MIT","source_url":"https://github.com/x/echo","download_url":"http://127.0.0.1/echo.wasm","sha256":"` + sha + `","min_goblog_version":"0.2.6","install_type":"wasm","runtime":"wasm","allowed_hosts":[],"released_at":"2026-09-15T00:00:00Z","detail_url":"","stars":7},
			{"name":"zeta","display_name":"Zeta","description":"Other thing","version":"0.1.0","author":"Someone","license":"MIT","source_url":"https://github.com/x/zeta","download_url":"https://example.test/z.go","sha256":"00","min_goblog_version":"0.1.0","install_type":"dynamic","released_at":"2026-01-01T00:00:00Z","detail_url":"","stars":1}]`))
		case "/echo.wasm":
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
	adp := &ad
	reg := plugin.NewRegistry(db)
	reg.Init()
	inst := &installer.Installer{
		Dir: t.TempDir(), WasmDir: t.TempDir(), Registry: reg, Directory: installer.NewFetcher(srv.Client()),
		Version: "v0.2.7", Client: rewritingClient(srv), Enabled: true, WasmEnabled: true,
		IndexURL: func() string { return srv.URL + "/index.json" },
	}
	adp.Installer = inst

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	router.GET("/api/v1/plugins/status", adp.PluginStatus)
	router.GET("/api/v1/plugins/directory", adp.PluginDirectory)
	router.POST("/api/v1/plugins/install", adp.InstallPlugin)
	router.POST("/api/v1/plugins/update", adp.UpdatePlugin)
	router.DELETE("/api/v1/plugins/:name", adp.UninstallPlugin)
	router.POST("/api/v1/plugins/refresh", adp.RefreshPluginDirectory)
	return &pluginsHarness{router: router, auth: a, inst: inst, srv: srv, ad: adp, db: db}
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
		{"DELETE", "/api/v1/plugins/echo"}, {"POST", "/api/v1/plugins/refresh"},
	} {
		if w := h.do(c.m, c.p, `{"name":"echo"}`); w.Code != http.StatusUnauthorized {
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
	if !st.DynamicEnabled || len(st.Available) != 2 || st.Available[0].Name != "echo" {
		t.Errorf("status = %+v", st)
	}
	if !st.DirWritable {
		t.Errorf("expected the happy-path plugins/wasm/ (a t.TempDir()) to be writable, got status = %+v", st)
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
	if len(avail) != 2 || avail[0].Name != "echo" || avail[1].Name != "zeta" {
		t.Errorf("sort=name = %+v", avail)
	}
	w = h.do("GET", "/api/v1/plugins/directory?sort=newest", "")
	json.Unmarshal(w.Body.Bytes(), &avail)
	if len(avail) != 2 || avail[0].Name != "echo" {
		t.Errorf("sort=newest = %+v", avail)
	}

	// Install.
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"echo"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Installed Echo v1.2.3") {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"echo"}`)
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
	w = h.do("POST", "/api/v1/plugins/update", `{"name":"echo"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update at latest should be 400, got %d %s", w.Code, w.Body.String())
	}

	// Uninstall.
	w = h.do("DELETE", "/api/v1/plugins/echo", "")
	if w.Code != http.StatusOK {
		t.Errorf("uninstall: %d %s", w.Code, w.Body.String())
	}
	w = h.do("DELETE", "/api/v1/plugins/echo", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("second uninstall: %d", w.Code)
	}

	// Disabled: 4xx with the message, status still fine.
	h.inst.WasmEnabled = false
	w = h.do("POST", "/api/v1/plugins/install", `{"name":"echo"}`)
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "ENABLE_WASM_PLUGINS") {
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
// fixture index (http://127.0.0.1/echo.wasm — plain HTTP is only accepted for
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

func adminFromHarness(h *pluginsHarness) *admin.Admin { return h.ad }

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
	for _, want := range []string{`id="tab-installed"`, `id="tab-browse"`, `/api/v1/plugins/status`, `href="/admin/plugins"`, "WebAssembly", "Talks to"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Directory/registry data (plugin names, URLs) must never be wired into
	// the page as inline JS via onclick/onchange — only via data-* attributes
	// read by a delegated listener, which the browser cannot mis-evaluate.
	if strings.Contains(body, `onclick="pluginAction`) || strings.Contains(body, `onchange="toggleEnabled`) {
		t.Errorf("page must not build inline onclick/onchange handlers from plugin data")
	}
	if !strings.Contains(body, "data-action=") {
		t.Errorf("page missing data-action= driven controls")
	}
	if !strings.Contains(body, `href="/admin/plugins/' + esc(safeName(p.name)) + '">Settings</a>`) {
		t.Errorf("installed rows must link Settings to the plugin's own page")
	}
}

// settingsPlugin is a compiled-in plugin with a text, a textarea and a
// password setting plus the enabled switch, for the per-plugin page.
type settingsPlugin struct{ plugin.BasePlugin }

func (settingsPlugin) Name() string        { return "hello-world" }
func (settingsPlugin) DisplayName() string { return "Hello World" }
func (settingsPlugin) Version() string     { return "1.0.0" }
func (settingsPlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{
		{Key: "enabled", Type: "text", DefaultValue: "true", Label: "Enabled"},
		{Key: "message", Type: "text", DefaultValue: "hi", Label: "Message", Description: "What to say."},
		{Key: "notes", Type: "textarea", DefaultValue: "", Label: "Notes"},
		{Key: "api_key", Type: "password", DefaultValue: "", Label: "API key"},
	}
}

// barePlugin declares no settings at all.
type barePlugin struct{ plugin.BasePlugin }

func (barePlugin) Name() string        { return "bare" }
func (barePlugin) DisplayName() string { return "Bare" }
func (barePlugin) Version() string     { return "0.0.1" }

// pluginPageHarness registers settingsPlugin and barePlugin and the
// per-plugin settings route with the default theme's templates.
func pluginPageHarness(t *testing.T) *pluginsHarness {
	t.Helper()
	h := newPluginsHarness(t)
	for _, p := range []plugin.Plugin{settingsPlugin{}, barePlugin{}} {
		if err := h.inst.Registry.RegisterDynamic(p, ""); err != nil {
			t.Fatal(err)
		}
		if err := h.inst.Registry.InitPlugin(p.Name()); err != nil {
			t.Fatal(err)
		}
	}
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	h.router.SetHTMLTemplate(tmpl)
	h.router.GET("/admin/plugins/:name", func(c *gin.Context) { adminFromHarness(h).AdminPluginSettings(c) })
	return h
}

func TestAdminPluginSettingsPage_NonAdmin(t *testing.T) {
	h := pluginPageHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	if w := h.do("GET", "/admin/plugins/hello-world", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestAdminPluginSettingsPage_Renders checks the page for a registered
// plugin: its name, the enabled switch and one input per setting using the
// <name>.<key> ids and JS hooks admin-script.js expects, with the password
// rendered blank so the stored secret never reaches the page.
func TestAdminPluginSettingsPage_Renders(t *testing.T) {
	h := pluginPageHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	h.inst.Registry.UpdateSetting("hello-world", "api_key", "s3cret")
	h.inst.Registry.UpdateSetting("hello-world", "message", "howdy")

	w := h.do("GET", "/admin/plugins/hello-world", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`class="admin-panel"`, `href="/admin/plugins">&larr; Back to Plugins</a>`, "<h1>Hello World", "(hello-world)",
		`id="hello-world.enabled" name="hello-world.enabled" data-plugin="hello-world" checked`, `onchange="togglePluginEnabled(this);"`,
		`<form class="plugin-settings-form" data-plugin="hello-world">`, `onclick="updatePluginSettings(this);"`,
		`id="hello-world.message" name="hello-world.message" value="howdy"`, "What to say.",
		`<textarea id="hello-world.notes" name="hello-world.notes"`,
		`type="password" id="hello-world.api_key" name="hello-world.api_key" value=""`, "leave blank to keep",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "s3cret") {
		t.Errorf("page leaks the stored password")
	}
	if n := strings.Count(body, `name="hello-world.enabled"`); n != 1 {
		t.Errorf("enabled switch rendered %d times, want once (it is not a form field)", n)
	}
	if strings.Contains(body, "no settings beyond") {
		t.Errorf("page claims the plugin has no settings")
	}
	if m := regexp.MustCompile(`class="h[1-6]"`).FindString(body); m != "" {
		t.Errorf("page uses Tachyons-clashing %s", m)
	}
}

// TestAdminPluginSettingsPage_NoSettings checks a plugin that declares no
// settings still gets a page with the enabled switch.
func TestAdminPluginSettingsPage_NoSettings(t *testing.T) {
	h := pluginPageHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	w := h.do("GET", "/admin/plugins/bare", "")
	if w.Code != http.StatusOK {
		t.Fatalf("page: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"<h1>Bare", `name="bare.enabled" data-plugin="bare"`, "no settings beyond"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, `name="bare.enabled" data-plugin="bare" checked`) {
		t.Errorf("a plugin never enabled renders its switch on")
	}
}

// TestAdminPluginSettingsPage_NotFound checks an unregistered name, and a
// name that breaks the plugin slug rule, both get the admin 404 page.
func TestAdminPluginSettingsPage_NotFound(t *testing.T) {
	h := pluginPageHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	h.auth.On("IsLoggedIn", mock.Anything).Return(true)
	for _, name := range []string{"nope", "Hello-World", "hello_world", "..", "a%20b"} {
		w := h.do("GET", "/admin/plugins/"+name, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", name, w.Code)
		}
		if !strings.Contains(w.Body.String(), "Plugin Not Found") {
			t.Errorf("%s: expected the error page, got %.200s", name, w.Body.String())
		}
	}
}
