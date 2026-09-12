package plugin_test

import (
	"goblog/plugin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestLoadDynamicPlugins_LoadsShippedExample loads the example dynamic plugin
// shipped in plugins/dynamic through the Yaegi loader, exactly as an operator
// would after copying it to a .go file, and checks it works end to end.
func TestLoadDynamicPlugins_LoadsShippedExample(t *testing.T) {
	src, err := os.ReadFile("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatalf("read shipped example: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), src, 0644); err != nil {
		t.Fatal(err)
	}
	// Files without a .go suffix and subdirectories must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.go.example"), src, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	registry := plugin.NewRegistry(db)
	plugin.LoadDynamicPlugins(registry, dir)

	plugins := registry.Plugins()
	if len(plugins) != 1 {
		t.Fatalf("expected exactly 1 plugin loaded, got %d", len(plugins))
	}
	p := plugins[0]
	if p.Name() != "hello" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("unexpected plugin metadata: name=%q display=%q version=%q", p.Name(), p.DisplayName(), p.Version())
	}
	if err := registry.Init(); err != nil {
		t.Fatalf("registry init: %v", err)
	}

	// Default settings are seeded and honoured: enabled by default with a default message.
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	data := registry.InjectTemplateData(c, "post.html", gin.H{})
	footer, _ := data["plugin_footer_html"].(string)
	if !strings.Contains(footer, "Hello from a dynamic plugin") {
		t.Errorf("expected the example's footer HTML, got %q", footer)
	}

	// Settings changed in the admin UI reach the plugin, and disabling it silences it.
	registry.UpdateSetting("hello", "message", "Custom <msg>")
	data = registry.InjectTemplateData(c, "post.html", gin.H{})
	footer, _ = data["plugin_footer_html"].(string)
	if !strings.Contains(footer, "Custom &lt;msg&gt;") {
		t.Errorf("expected updated, HTML-escaped message in footer, got %q", footer)
	}
	registry.UpdateSetting("hello", "enabled", "false")
	data = registry.InjectTemplateData(c, "post.html", gin.H{})
	if footer, _ := data["plugin_footer_html"].(string); footer != "" {
		t.Errorf("expected no footer HTML when disabled, got %q", footer)
	}
}

// TestLoadDynamicPlugins_SkipsBrokenFiles checks one bad plugin doesn't stop
// the rest from loading, and a missing directory is not an error.
func TestLoadDynamicPlugins_SkipsBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatalf("read shipped example: %v", err)
	}
	files := map[string][]byte{
		"broken.go":   []byte("package main\nfunc NewPlugin() int { return 1 }\n"),
		"syntax.go":   []byte("package main\nfunc NewPlugin( {\n"),
		"zz_hello.go": src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	registry := plugin.NewRegistry(nil)
	plugin.LoadDynamicPlugins(registry, dir)
	if got := len(registry.Plugins()); got != 1 {
		t.Fatalf("expected only the valid plugin to load, got %d", got)
	}

	empty := plugin.NewRegistry(nil)
	plugin.LoadDynamicPlugins(empty, filepath.Join(dir, "does-not-exist"))
	if got := len(empty.Plugins()); got != 0 {
		t.Fatalf("expected no plugins from a missing directory, got %d", got)
	}
}
