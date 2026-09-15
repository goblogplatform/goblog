package plugin_test

import (
	"goblog/plugin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
