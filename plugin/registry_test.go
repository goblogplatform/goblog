package plugin_test

import (
	"errors"
	"goblog/plugin"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type testPlugin struct {
	plugin.BasePlugin
}

func (p *testPlugin) Name() string        { return "test" }
func (p *testPlugin) DisplayName() string { return "Test Plugin" }
func (p *testPlugin) Version() string     { return "0.1.0" }

func (p *testPlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{
		{Key: "api_key", Type: "text", DefaultValue: "default123", Label: "API Key"},
	}
}

func (p *testPlugin) TemplateHead(ctx *plugin.HookContext) string {
	if key := ctx.Settings["api_key"]; key != "" {
		return "<!-- test-head:" + key + " -->"
	}
	return ""
}

func (p *testPlugin) TemplateFooter(ctx *plugin.HookContext) string {
	return "<!-- test-footer -->"
}

func (p *testPlugin) TemplateData(ctx *plugin.HookContext) gin.H {
	return gin.H{"greeting": "hello from test plugin"}
}

func TestRegistryBasics(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry(db)
	tp := &testPlugin{}
	reg.Register(tp)

	if len(reg.Plugins()) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(reg.Plugins()))
	}
	if reg.Plugins()[0].Name() != "test" {
		t.Fatalf("expected plugin name 'test', got %q", reg.Plugins()[0].Name())
	}
}

func TestRegistryInit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry(db)
	reg.Register(&testPlugin{})
	if err := reg.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Check setting was seeded
	var setting plugin.PluginSetting
	db.Where("plugin_name = ? AND key = ?", "test", "api_key").First(&setting)
	if setting.Value != "default123" {
		t.Fatalf("expected default value 'default123', got %q", setting.Value)
	}
}

func TestRegistryInjectTemplateData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry(db)
	reg.Register(&testPlugin{})
	reg.Init()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/", nil)

	data := gin.H{"title": "Test Page"}
	data = reg.InjectTemplateData(c, "home.html", data)

	// Check plugin data was injected
	plugins, ok := data["plugins"].(gin.H)
	if !ok {
		t.Fatal("expected plugins key in data")
	}
	testData, ok := plugins["test"].(gin.H)
	if !ok {
		t.Fatal("expected test plugin data")
	}
	if testData["greeting"] != "hello from test plugin" {
		t.Fatalf("expected greeting, got %v", testData["greeting"])
	}

	// Check head/footer HTML
	headHTML, ok := data["plugin_head_html"].(string)
	if !ok || headHTML == "" {
		t.Fatal("expected plugin_head_html")
	}
	if headHTML != "<!-- test-head:default123 -->" {
		t.Fatalf("unexpected head HTML: %q", headHTML)
	}

	footerHTML, ok := data["plugin_footer_html"].(string)
	if !ok || footerHTML != "<!-- test-footer -->" {
		t.Fatalf("unexpected footer HTML: %q", footerHTML)
	}
}

func TestGetAllSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry(db)
	reg.Register(&testPlugin{})
	reg.Init()

	groups := reg.GetAllSettings()
	if len(groups) != 1 {
		t.Fatalf("expected 1 settings group, got %d", len(groups))
	}
	if groups[0].PluginName != "test" {
		t.Fatalf("expected plugin name 'test', got %q", groups[0].PluginName)
	}
	if groups[0].CurrentValues["api_key"] != "default123" {
		t.Fatalf("expected current value 'default123', got %q", groups[0].CurrentValues["api_key"])
	}
}

// pagePlugin owns a page and uses SubPath: "" renders a template, "data.json"
// writes JSON itself, anything else is declined.
type pagePlugin struct {
	plugin.BasePlugin
}

func (p *pagePlugin) Name() string        { return "pager" }
func (p *pagePlugin) DisplayName() string { return "Pager" }
func (p *pagePlugin) Version() string     { return "1.0.0" }
func (p *pagePlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{{Key: "enabled", Type: "text", DefaultValue: "true", Label: "Enabled"}}
}
func (p *pagePlugin) Pages() []plugin.PageDefinition {
	return []plugin.PageDefinition{{PageType: "pager", Title: "Pager", Slug: "pager"}}
}
func (p *pagePlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	switch ctx.SubPath {
	case "":
		return "page_content.html", gin.H{"plugin_content": "root"}
	case "data.json":
		ctx.GinContext.JSON(http.StatusOK, gin.H{"ok": true})
		return "", nil
	}
	return "", nil
}

func TestRenderPluginPage_SubPathsAndRawResponses(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	newCtx := func(path string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		return c, w
	}

	// The page itself: template + data, SubPath is empty.
	c, _ := newCtx("/pager")
	tmpl, data, handled := reg.RenderPluginPage(c, "pager", "")
	if !handled || tmpl != "page_content.html" || data["plugin_content"] != "root" {
		t.Errorf("root: handled=%v tmpl=%q data=%v", handled, tmpl, data)
	}

	// A sub-path the plugin answers by writing the response: handled, no template.
	c, w := newCtx("/pager/data.json")
	tmpl, _, handled = reg.RenderPluginPage(c, "pager", "data.json")
	if !handled || tmpl != "" {
		t.Errorf("data.json: handled=%v tmpl=%q", handled, tmpl)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("data.json: body=%q content-type=%q", w.Body.String(), w.Header().Get("Content-Type"))
	}

	// A sub-path the plugin declines: not handled, nothing written.
	c, w = newCtx("/pager/nope")
	if _, _, handled = reg.RenderPluginPage(c, "pager", "nope"); handled || w.Body.Len() != 0 {
		t.Errorf("nope: handled=%v body=%q", handled, w.Body.String())
	}

	// Disabled plugin: never called.
	reg.UpdateSetting("pager", "enabled", "false")
	c, _ = newCtx("/pager")
	if _, _, handled = reg.RenderPluginPage(c, "pager", ""); handled {
		t.Error("disabled plugin should not handle its page")
	}
}

// jobPlugin counts how often its 10ms job runs.
type jobPlugin struct {
	plugin.BasePlugin
	name string
	runs atomic.Int32
}

func (p *jobPlugin) Name() string        { return p.name }
func (p *jobPlugin) DisplayName() string { return "Job " + p.name }
func (p *jobPlugin) Version() string     { return "1.0.0" }
func (p *jobPlugin) ScheduledJobs() []plugin.ScheduledJob {
	return []plugin.ScheduledJob{{Name: "tick", Interval: 10 * time.Millisecond, Run: func(*gorm.DB, map[string]string) error {
		p.runs.Add(1)
		return nil
	}}}
}

// failInitPlugin fails OnInit.
type failInitPlugin struct{ plugin.BasePlugin }

func (failInitPlugin) Name() string          { return "failing" }
func (failInitPlugin) DisplayName() string   { return "Failing" }
func (failInitPlugin) Version() string       { return "0.0.1" }
func (failInitPlugin) OnInit(*gorm.DB) error { return errors.New("boom") }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestRegistry(t *testing.T) *plugin.Registry {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	return plugin.NewRegistry(db)
}

func hasPlugin(reg *plugin.Registry, name string) bool {
	for _, p := range reg.Plugins() {
		if p.Name() == name {
			return true
		}
	}
	return false
}

func TestUnregisterStopsOnlyThatPluginsJobs(t *testing.T) {
	reg := newTestRegistry(t)
	a, b := &jobPlugin{name: "a"}, &jobPlugin{name: "b"}
	reg.Register(a)
	if err := reg.RegisterDynamic(b, "/tmp/b.go"); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDynamic(&jobPlugin{name: "b"}, "/tmp/b2.go"); err == nil {
		t.Error("registering a second plugin named b should fail")
	}
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	reg.StartScheduledJobs()
	reg.StartScheduledJobs() // idempotent
	waitFor(t, "both jobs to run", func() bool { return a.runs.Load() > 0 && b.runs.Load() > 0 })

	dyn := reg.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "b" || dyn[0].Path != "/tmp/b.go" || dyn[0].DisplayName != "Job b" {
		t.Errorf("Dynamic() = %+v", dyn)
	}
	if dyn[0].Runtime != "go" {
		t.Errorf("runtime = %q", dyn[0].Runtime)
	}

	if err := reg.Unregister("b"); err != nil {
		t.Fatal(err)
	}
	if hasPlugin(reg, "b") || len(reg.Dynamic()) != 0 {
		t.Error("b should be gone after Unregister")
	}
	time.Sleep(30 * time.Millisecond)
	n := b.runs.Load()
	time.Sleep(50 * time.Millisecond)
	if b.runs.Load() != n {
		t.Error("b's job kept running after Unregister")
	}
	an := a.runs.Load()
	waitFor(t, "a's job to keep running", func() bool { return a.runs.Load() > an })

	if err := reg.Unregister("nope"); err == nil {
		t.Error("unregistering an unknown plugin should fail")
	}
	reg.Stop()
	time.Sleep(30 * time.Millisecond)
	an = a.runs.Load()
	time.Sleep(50 * time.Millisecond)
	if a.runs.Load() != an {
		t.Error("a's job kept running after Stop")
	}
}

func TestInitContinuesPastFailingPlugin(t *testing.T) {
	reg := newTestRegistry(t)
	reg.Register(failInitPlugin{})
	reg.Register(&testPlugin{})
	err := reg.Init()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the failing plugin's error, got %v", err)
	}
	for _, g := range reg.GetAllSettings() {
		if g.PluginName == "test" && g.CurrentValues["api_key"] == "default123" {
			return
		}
	}
	t.Error("the plugin registered after the failing one should still have its settings seeded")
}

func TestInitPluginAndDeleteSettings(t *testing.T) {
	reg := newTestRegistry(t)
	reg.Register(&testPlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	reg.StartScheduledJobs()

	late := &jobPlugin{name: "late"}
	if err := reg.RegisterDynamic(late, "late.go"); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitPlugin("late"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "late plugin's job", func() bool { return late.runs.Load() > 0 })
	if err := reg.InitPlugin("nope"); err == nil {
		t.Error("InitPlugin on an unknown plugin should fail")
	}

	reg.DeleteSettings("test")
	for _, g := range reg.GetAllSettings() {
		if g.PluginName == "test" && len(g.CurrentValues) != 0 {
			t.Errorf("settings for test should be deleted, got %v", g.CurrentValues)
		}
	}
	reg.Stop()
}
