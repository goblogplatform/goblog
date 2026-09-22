package plugin_test

import (
	"errors"
	"goblog/blog"
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

// TestPluginSettings checks the one-plugin lookup the per-plugin admin page
// uses: a registered name gives its definitions and stored values, a plugin
// with no settings is still found (its enabled switch is always shown), and
// an unknown name reports not found.
func TestPluginSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}

	reg := plugin.NewRegistry(db)
	reg.Register(&testPlugin{})
	reg.Register(&noSettingsPlugin{})
	reg.Init()
	reg.UpdateSetting("test", "enabled", "true")

	g, ok := reg.PluginSettings("test")
	if !ok || g.PluginName != "test" || g.DisplayName != "Test Plugin" || len(g.Settings) != 1 {
		t.Fatalf("PluginSettings(test) = %+v, %v", g, ok)
	}
	if g.CurrentValues["api_key"] != "default123" || g.CurrentValues["enabled"] != "true" {
		t.Errorf("current values = %v", g.CurrentValues)
	}
	if g, ok := reg.PluginSettings("bare"); !ok || len(g.Settings) != 0 || g.CurrentValues == nil {
		t.Errorf("PluginSettings(bare) = %+v, %v; want found with no definitions", g, ok)
	}
	if g, ok := reg.PluginSettings("nope"); ok {
		t.Errorf("PluginSettings(nope) = %+v, want not found", g)
	}
}

// noSettingsPlugin declares nothing but its identity.
type noSettingsPlugin struct{ plugin.BasePlugin }

func (noSettingsPlugin) Name() string        { return "bare" }
func (noSettingsPlugin) DisplayName() string { return "Bare" }
func (noSettingsPlugin) Version() string     { return "0.0.1" }

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

// TestInit_CreatesPageForPluginsThatCannotTouchTheDB covers wasm plugins
// (sandboxed) and Yaegi plugins (can't implement a gorm-typed OnInit): they
// declare a page via Pages() but cannot create the blog.Page row themselves,
// so the registry must do it, the same way plugins/directory's own OnInit
// does for itself.
func TestInit_CreatesPageForPluginsThatCannotTouchTheDB(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	var page blog.Page
	if err := db.Where("page_type = ?", "pager").First(&page).Error; err != nil {
		t.Fatalf("expected a page row for the plugin's declared page, got: %v", err)
	}
	if page.Slug != "pager" || page.Title != "Pager" || !page.Enabled {
		t.Errorf("page = %+v", page)
	}

	// Idempotent: re-running Init (as a hot install's InitPlugin does) must
	// not create a duplicate.
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("page_type = ?", "pager").Count(&count)
	if count != 1 {
		t.Errorf("expected exactly one page row after a second Init, got %d", count)
	}
}

// TestInit_LeavesSlugCollisionToTheOperator mirrors
// directory.TestOnInit_SlugCollision: a slug already used by a different
// page type is left alone rather than erroring out plugin init.
func TestInit_LeavesSlugCollisionToTheOperator(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&blog.Page{Title: "Mine", Slug: "pager", PageType: "custom", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("page_type = ?", "pager").Count(&count)
	if count != 0 {
		t.Errorf("expected the pre-existing page to be left alone, got %d pager-typed pages", count)
	}
}

// TestInit_LeavesPostTypeSlugToTheOperator: a post type already listed at
// the slug keeps its URL (blog resolves a page before a post type, so the
// plugin's page would shadow it).
func TestInit_LeavesPostTypeSlugToTheOperator(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&blog.PostType{Name: "Pager", Slug: "pager"}).Error; err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("slug = ?", "pager").Count(&count)
	if count != 0 {
		t.Errorf("expected the post type's slug to be left alone, got %d pages at it", count)
	}
}

// slugPlugin declares one page at an arbitrary slug.
type slugPlugin struct {
	plugin.BasePlugin
	slug string
}

func (p *slugPlugin) Name() string        { return "slugger" }
func (p *slugPlugin) DisplayName() string { return "Slugger" }
func (p *slugPlugin) Version() string     { return "1.0.0" }
func (p *slugPlugin) Pages() []plugin.PageDefinition {
	return []plugin.PageDefinition{{PageType: "slugger", Title: "Slugger", Slug: p.slug}}
}

// TestInit_RefusesReservedAndMalformedSlugs: a plugin cannot claim a
// top-level path goblog serves itself, nor one that is not a plain path
// segment.
func TestInit_RefusesReservedAndMalformedSlugs(t *testing.T) {
	for _, slug := range []string{"admin", "api", "login", "logout", "search", "theme", "wizard", "comments", "wizard_db", "test_db", "wp-content", "posts", "tags", "sitemap.xml", "Bad Slug", "a/b", "../x"} {
		db, err := gorm.Open(sqlite.Open(":memory:"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
			t.Fatal(err)
		}
		reg := plugin.NewRegistry(db)
		reg.Register(&slugPlugin{slug: slug})
		if err := reg.Init(); err != nil {
			t.Fatal(err)
		}
		var count int64
		db.Model(&blog.Page{}).Where("page_type = ?", "slugger").Count(&count)
		if count != 0 {
			t.Errorf("slug %q: expected no page to be created, got %d", slug, count)
		}
	}
}

// TestDeletePages removes what ensurePages created (uninstall) and leaves
// other pages alone.
func TestDeletePages(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&blog.Page{Title: "About", Slug: "about", PageType: "about", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	if err := reg.DeletePages(nil); err != nil {
		t.Errorf("DeletePages(nil): %v", err)
	}
	if err := reg.Unregister("pager"); err != nil { // as Uninstall does before DeletePages
		t.Fatal(err)
	}
	if err := reg.DeletePages([]string{"pager"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("page_type = ?", "pager").Count(&count)
	if count != 0 {
		t.Errorf("expected the plugin's page to be deleted, got %d", count)
	}
	db.Model(&blog.Page{}).Count(&count)
	if count != 1 {
		t.Errorf("expected the about page to survive, got %d pages", count)
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

// otherPagerPlugin claims pagePlugin's "pager" page type under another name.
type otherPagerPlugin struct{ pagePlugin }

func (p *otherPagerPlugin) Name() string { return "pager2" }

// TestRegisterDynamic_RejectsClaimedPageType: a plugin cannot take over a
// page type another registered plugin already declares — it would shadow
// that plugin's page and, on uninstall, delete its page row.
func TestRegisterDynamic_RejectsClaimedPageType(t *testing.T) {
	reg := plugin.NewRegistry(nil)
	reg.Register(&pagePlugin{})
	err := reg.RegisterDynamic(&otherPagerPlugin{}, "/x/pager2.wasm")
	if err == nil || !strings.Contains(err.Error(), "pager") {
		t.Fatalf("expected a page-type conflict error, got %v", err)
	}
	if len(reg.Plugins()) != 1 {
		t.Error("the conflicting plugin must not be registered")
	}
}

// TestDeletePages_KeepsClaimedTypes: pages whose type another registered
// plugin still declares are left alone.
func TestDeletePages_KeepsClaimedTypes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}, &blog.Page{}, &blog.PostType{}); err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	reg.Register(&pagePlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	if err := reg.DeletePages([]string{"pager"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("page_type = ?", "pager").Count(&count)
	if count != 1 {
		t.Errorf("expected the still-registered plugin's page to survive, got %d", count)
	}
	if err := reg.Unregister("pager"); err != nil {
		t.Fatal(err)
	}
	if err := reg.DeletePages([]string{"pager"}); err != nil {
		t.Fatal(err)
	}
	db.Model(&blog.Page{}).Where("page_type = ?", "pager").Count(&count)
	if count != 0 {
		t.Errorf("expected the page to be deleted once unclaimed, got %d", count)
	}
}

// searchPlugin answers site searches with one fixed result.
type searchPlugin struct {
	plugin.BasePlugin
	gotQuery string
}

func (p *searchPlugin) Name() string        { return "searcher" }
func (p *searchPlugin) DisplayName() string { return "Searcher" }
func (p *searchPlugin) Version() string     { return "0.1.0" }
func (p *searchPlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{{Key: "enabled", Type: "text", DefaultValue: "false", Label: "Enabled"}}
}
func (p *searchPlugin) Search(ctx *plugin.HookContext, query string) []plugin.SearchResult {
	p.gotQuery = query
	if ctx.Settings["enabled"] != "true" {
		panic("disabled plugins must not be searched")
	}
	return []plugin.SearchResult{{Title: "Hit", URL: "/x/hit", Summary: "found " + query, Kind: "Thing"}}
}

// TestRegistrySearch: Search asks every enabled plugin that implements
// Searcher and concatenates their results; plugins that do not implement
// it, or are disabled, contribute nothing.
func TestRegistrySearch(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&plugin.PluginSetting{})
	reg := plugin.NewRegistry(db)
	sp := &searchPlugin{}
	reg.Register(&testPlugin{}) // not a Searcher
	reg.Register(sp)
	reg.Init()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/search?q=hi", nil)

	if got := reg.Search(c, "hi"); len(got) != 0 {
		t.Errorf("disabled searcher must contribute nothing, got %v", got)
	}
	reg.UpdateSetting("searcher", "enabled", "true")
	got := reg.Search(c, "hi")
	if len(got) != 1 || got[0].Title != "Hit" || got[0].URL != "/x/hit" || got[0].Summary != "found hi" || got[0].Kind != "Thing" {
		t.Fatalf("results = %+v", got)
	}
	if sp.gotQuery != "hi" {
		t.Errorf("query passed = %q", sp.gotQuery)
	}
}

// TestPageSlug: the slug of a plugin page as the admin has it, or the
// default when the page row is missing or there is no database.
func TestPageSlug(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	if got := plugin.PageSlug(db, "dir", "plugins"); got != "plugins" {
		t.Errorf("missing row: %q", got)
	}
	db.Create(&blog.Page{Title: "Dir", Slug: "extensions", PageType: "dir", Enabled: true})
	if got := plugin.PageSlug(db, "dir", "plugins"); got != "extensions" {
		t.Errorf("renamed: %q", got)
	}
	if got := plugin.PageSlug(nil, "dir", "plugins"); got != "plugins" {
		t.Errorf("nil db: %q", got)
	}
}

type sitemapPlugin struct{ searchPlugin }

func (p *sitemapPlugin) Sitemap(ctx *plugin.HookContext) []plugin.SitemapURL {
	if ctx.Settings["enabled"] != "true" {
		panic("disabled plugins must not be asked for sitemap URLs")
	}
	return []plugin.SitemapURL{{Loc: "/x/hit"}}
}

// TestRegistrySitemap: SitemapURLs asks every enabled plugin that
// implements Sitemapper; the rest contribute nothing.
func TestRegistrySitemap(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&plugin.PluginSetting{})
	reg := plugin.NewRegistry(db)
	reg.Register(&testPlugin{})
	reg.Register(&sitemapPlugin{})
	reg.Init()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/sitemap.xml", nil)
	if got := reg.SitemapURLs(c); len(got) != 0 {
		t.Errorf("disabled: %v", got)
	}
	reg.UpdateSetting("searcher", "enabled", "true")
	if got := reg.SitemapURLs(c); len(got) != 1 || got[0].Loc != "/x/hit" {
		t.Errorf("enabled: %v", got)
	}
}
