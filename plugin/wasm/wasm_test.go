package wasm

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// memStore is an in-memory plugin.Store.
type memStore struct {
	mu sync.Mutex
	m  map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string]map[string][]byte{}} }
func (s *memStore) Get(p, k string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[p][k]
	return v, ok, nil
}
func (s *memStore) Set(p, k string, v []byte) error {
	if len(k) == 0 || len(k) > plugin.MaxStoreKeyBytes || len(v) > plugin.MaxStoreValueBytes {
		return errors.New("limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[p] == nil {
		s.m[p] = map[string][]byte{}
	}
	s.m[p][k] = append([]byte(nil), v...)
	return nil
}
func (s *memStore) Delete(p, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m[p], k)
	return nil
}
func (s *memStore) List(p, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.m[p] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *memStore) DeleteAll(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, p)
	return nil
}

func echoBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/echo.wasm")
	if err != nil {
		t.Fatalf("fixture missing (run go generate ./plugin/wasm): %v", err)
	}
	return b
}

func loadEcho(t *testing.T, opts Options) *Plugin {
	t.Helper()
	if opts.Store == nil {
		opts.Store = newMemStore()
	}
	p, err := LoadBytes(echoBytes(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func hookCtx(settings map[string]string, path, subPath string) *plugin.HookContext {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return &plugin.HookContext{GinContext: c, Settings: settings, Template: "post.html", SubPath: subPath}
}

func TestLoad_IdentitySettingsPagesJobs(t *testing.T) {
	p := loadEcho(t, Options{})
	if p.Name() != "echo" || p.DisplayName() != "Echo" || p.Version() != "1.2.3" {
		t.Errorf("identity = %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	s := p.Settings()
	if len(s) != 2 || s[0].Key != "enabled" || s[0].DefaultValue != "true" || s[1].Key != "greeting" || s[1].Label != "Greeting" {
		t.Errorf("settings = %+v", s)
	}
	pg := p.Pages()
	if len(pg) != 1 || pg[0].PageType != "echo" || pg[0].Slug != "echo" || !pg[0].ShowInNav || pg[0].NavOrder != 5 {
		t.Errorf("pages = %+v", pg)
	}
	j := p.ScheduledJobs()
	if len(j) != 1 || j[0].Name != "tick" || j[0].Interval != 60*time.Second {
		t.Errorf("jobs = %+v", j)
	}
	var _ plugin.Plugin = p
}

func TestLoad_RejectsNonPluginAndMissingIdentity(t *testing.T) {
	if _, err := LoadBytes([]byte("not wasm"), Options{}); err == nil {
		t.Error("garbage should fail to load")
	}
	// A valid module without the identity export: use a minimal hand-written wasm.
	empty := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00} // "\0asm" v1, no sections
	if _, err := LoadBytes(empty, Options{}); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("expected ErrNoIdentity, got %v", err)
	}
}

func TestHooks(t *testing.T) {
	p := loadEcho(t, Options{})
	enabled := map[string]string{"enabled": "true", "greeting": "hello"}
	if got := p.TemplateFooter(hookCtx(enabled, "/", "")); got != "<p>hello</p>" {
		t.Errorf("footer = %q", got)
	}
	if got := p.TemplateHead(hookCtx(enabled, "/", "")); got != "<!-- echo head -->" {
		t.Errorf("head = %q", got)
	}
	data := p.TemplateData(hookCtx(enabled, "/", ""))
	if data["greeting"] != "hello" || data["template"] != "post.html" {
		t.Errorf("data = %v", data)
	}
	disabled := map[string]string{"enabled": "false", "greeting": "hello"}
	if p.TemplateFooter(hookCtx(disabled, "/", "")) != "" || p.TemplateHead(hookCtx(disabled, "/", "")) != "" || p.TemplateData(hookCtx(disabled, "/", "")) != nil {
		t.Error("disabled plugin hooks must be no-ops")
	}
}

func TestRenderPage(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true", "greeting": "yo"}

	tmpl, data := p.RenderPage(hookCtx(settings, "/echo", ""), "echo")
	if tmpl != "page_content.html" || data["has_plugin_content"] != true || data["plugin_content"] != "<h2>echo</h2>yo" {
		t.Errorf("html: %q %v", tmpl, data)
	}
	tmpl, data = p.RenderPage(hookCtx(settings, "/echo/tmpl", "tmpl"), "echo")
	if tmpl != "page_content.html" || data["plugin_content"] != "from template" {
		t.Errorf("template: %q %v", tmpl, data)
	}
	ctx := hookCtx(settings, "/echo/data.json", "data.json")
	tmpl, _ = p.RenderPage(ctx, "echo")
	w := ctx.GinContext.Writer.(interface{ Status() int })
	if tmpl != "" || w.Status() != 200 || !ctx.GinContext.Writer.Written() {
		t.Errorf("raw: tmpl=%q status=%d written=%v", tmpl, w.Status(), ctx.GinContext.Writer.Written())
	}
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/nope", "nope"), "echo"); tmpl != "" {
		t.Errorf("unknown sub-path should be declined, got %q", tmpl)
	}
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "other"); tmpl != "" {
		t.Errorf("other page type should be declined, got %q", tmpl)
	}
	// Plugin error → declined, not a panic.
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/boom", "boom"), "echo"); tmpl != "" {
		t.Errorf("plugin error should decline, got %q", tmpl)
	}
}

func TestStoreHostFunctions(t *testing.T) {
	st := newMemStore()
	p := loadEcho(t, Options{Store: st})
	settings := map[string]string{"enabled": "true"}
	ctx := hookCtx(settings, "/echo/store?v=1", "store")
	_, data := p.RenderPage(ctx, "echo")
	html, _ := data["plugin_content"].(string)
	if html != `got=v-1 keys=["k"] after=[]` {
		t.Errorf("store round trip = %q", html)
	}
	// Namespaced by plugin name.
	if _, found, _ := st.Get("echo", "k"); found {
		t.Error("k should have been deleted")
	}
	// Jobs and on_init reach the store with the plugin's settings.
	if err := p.OnInit(nil); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "init"); string(v) != "1" {
		t.Error("on_init should have written init=1")
	}
	if err := p.ScheduledJobs()[0].Run((*gorm.DB)(nil), map[string]string{"enabled": "true", "greeting": "g"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "job_ran"); string(v) != "tick/g" {
		t.Errorf("job_ran = %q", v)
	}
	if err := p.ScheduledJobs()[0].Run((*gorm.DB)(nil), map[string]string{"enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	// Disabled: job must not run (value unchanged).
	if v, _, _ := st.Get("echo", "job_ran"); string(v) != "tick/g" {
		t.Error("disabled plugin's job must not run")
	}
}

func TestHTTPAllowedHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("pong")) }))
	defer srv.Close()
	settings := map[string]string{"enabled": "true"}

	allowed := loadEcho(t, Options{AllowedHosts: []string{"127.0.0.1"}})
	_, data := allowed.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+"/ping", "http"), "echo")
	if html, _ := data["plugin_content"].(string); html != "status=200 body=pong" {
		t.Errorf("allowed host: %q", html)
	}
	denied := loadEcho(t, Options{})
	if tmpl, _ := denied.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+"/ping", "http"), "echo"); tmpl != "" {
		t.Errorf("no allowed hosts → request must fail and the page decline, got %q", tmpl)
	}
}

func TestTimeoutAndMemoryCap(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true"}
	old := callTimeout
	callTimeout = 300 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	start := time.Now()
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/spin", "spin"), "echo"); tmpl != "" {
		t.Error("spinning plugin should be interrupted and decline")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout did not interrupt the plugin")
	}
	// After a timeout the instance is unusable; the adapter must report that
	// clearly rather than hang or panic.
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "" {
		t.Log("instance recovered after timeout (acceptable)")
	}
	p2 := loadEcho(t, Options{})
	if tmpl, _ := p2.RenderPage(hookCtx(settings, "/echo/big", "big"), "echo"); tmpl != "" {
		t.Error("200 MB allocation should exceed the memory cap and decline")
	}
}

func TestLogging(t *testing.T) {
	var logged []string
	p := loadEcho(t, Options{Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	p.RenderPage(hookCtx(map[string]string{"enabled": "true"}, "/echo/log", "log"), "echo")
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "plugin echo") || !strings.Contains(joined, "hello from echo") {
		t.Errorf("expected the plugin's log line prefixed with its name, got %q", joined)
	}
}
