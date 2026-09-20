package wasm

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"goblog/plugin"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
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

func TestRenderPage_RawStatus(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true"}

	// A status outside 100..599 would panic inside net/http: decline instead.
	ctx := hookCtx(settings, "/echo/raw-badstatus", "raw-badstatus")
	if tmpl, _ := p.RenderPage(ctx, "echo"); tmpl != "" || ctx.GinContext.Writer.Written() {
		t.Errorf("bad status: tmpl=%q written=%v", tmpl, ctx.GinContext.Writer.Written())
	}
	// Missing status/content_type default to 200 text/plain.
	ctx = hookCtx(settings, "/echo/raw-defaults", "raw-defaults")
	rec := httptest.NewRecorder()
	ctx.GinContext, _ = gin.CreateTestContext(rec)
	ctx.GinContext.Request = httptest.NewRequest(http.MethodGet, "/echo/raw-defaults", nil)
	if tmpl, _ := p.RenderPage(ctx, "echo"); tmpl != "" {
		t.Errorf("raw defaults: tmpl=%q", tmpl)
	}
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Body.String() != "plain" {
		t.Errorf("raw defaults: status=%d ct=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestClockAndRandom(t *testing.T) {
	p := loadEcho(t, Options{})
	settings := map[string]string{"enabled": "true"}
	_, data := p.RenderPage(hookCtx(settings, "/echo/now", "now"), "echo")
	got, _ := data["plugin_content"].(string)
	unix, err := strconv.ParseInt(got, 10, 64)
	if err != nil {
		t.Fatalf("now = %q: %v", got, err)
	}
	if d := time.Since(time.Unix(unix, 0)); d < -120*time.Second || d > 120*time.Second {
		t.Errorf("plugin clock is %v off the host clock (wazero fake clock?)", d)
	}
	_, d1 := p.RenderPage(hookCtx(settings, "/echo/rand", "rand"), "echo")
	_, d2 := p.RenderPage(hookCtx(settings, "/echo/rand", "rand"), "echo")
	r1, _ := d1["plugin_content"].(string)
	r2, _ := d2["plugin_content"].(string)
	if len(r1) != 32 || r1 == r2 {
		t.Errorf("random bytes should differ between calls: %q %q", r1, r2)
	}
}

func TestStoreHostFunctions(t *testing.T) {
	st := newMemStore()
	p := loadEcho(t, Options{Store: st})
	settings := map[string]string{"enabled": "true"}
	ctx := hookCtx(settings, "/echo/store?v=1", "store")
	_, data := p.RenderPage(ctx, "echo")
	html, _ := data["plugin_content"].(string)
	if html != `got=v-1 keys=["k"] after=[missing]` {
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
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			// Same server, different host name: reachable, but not on the
			// plugin's allow-list. Without a redirect check the plugin would
			// get "pong" from a host it was never allowed to talk to.
			u, _ := url.Parse(srv.URL)
			http.Redirect(w, r, "http://localhost:"+u.Port()+"/ping", http.StatusFound)
		case "/redirect-invalid":
			http.Redirect(w, r, "http://example.invalid/x", http.StatusFound)
		case "/redirect-same":
			http.Redirect(w, r, "/ping", http.StatusFound)
		default:
			w.Write([]byte("pong"))
		}
	}))
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

	// Redirects are re-checked against the allow-list on every hop.
	for _, sub := range []string{"/redirect", "/redirect-invalid"} {
		if tmpl, data := allowed.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+sub, "http"), "echo"); tmpl != "" {
			t.Errorf("%s: a redirect off the allowed hosts must fail and the page decline, got %q %v", sub, tmpl, data)
		}
	}
	_, data = allowed.RenderPage(hookCtx(settings, "/echo/http?url="+srv.URL+"/redirect-same", "http"), "echo")
	if html, _ := data["plugin_content"].(string); html != "status=200 body=pong" {
		t.Errorf("redirect within the allowed host: %q", html)
	}
}

func TestCheckRedirect(t *testing.T) {
	if http.DefaultClient.CheckRedirect == nil {
		t.Fatal("init should have installed the redirect check on http.DefaultClient")
	}
	for _, tc := range []struct {
		allowed []string
		host    string
		ok      bool
	}{
		{[]string{"api.example.test"}, "api.example.test", true},
		{[]string{"api.example.test"}, "evil.example.test", false},
		{[]string{"*.example.test"}, "api.example.test", true},
		{[]string{"*.example.test"}, "example.org", false},
		{[]string{"*"}, "anything.example", true},
		{nil, "api.example.test", false},
	} {
		if got := hostAllowed(tc.allowed, tc.host); got != tc.ok {
			t.Errorf("hostAllowed(%v, %q) = %v, want %v", tc.allowed, tc.host, got, tc.ok)
		}
	}
	// No plugin on the context: net/http's default 10-hop rule.
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	if err := checkRedirect(req, make([]*http.Request, 9)); err != nil {
		t.Errorf("9 hops without a plugin should be fine: %v", err)
	}
	if err := checkRedirect(req, make([]*http.Request, 10)); err == nil {
		t.Error("10 hops should stop")
	}
}

func TestOnInit_StoredSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&plugin.PluginSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&plugin.PluginSetting{PluginName: "echo", Key: "greeting", Value: "custom"}).Error; err != nil {
		t.Fatal(err)
	}
	st := newMemStore()
	p := loadEcho(t, Options{Store: st})
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "init_greeting"); string(v) != "custom" {
		t.Errorf("on_init should see the stored value, got greeting=%q", v)
	}
	// Without a db the declared defaults are what on_init gets.
	if err := p.OnInit(nil); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.Get("echo", "init_greeting"); string(v) != "hi" {
		t.Errorf("on_init without a db should see the default, got greeting=%q", v)
	}
}

func TestTimeoutAndMemoryCap(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	p := loadEcho(t, Options{Logf: func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(f, a...))
	}})
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
	// The timeout closed the instance; the next call re-creates it and the
	// plugin keeps working.
	if tmpl, data := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "page_content.html" || data["plugin_content"] != "<h2>echo</h2>" {
		t.Errorf("instance should have been re-created after the timeout, got %q %v", tmpl, data)
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "plugin echo: instance re-created after it was closed") {
		t.Errorf("expected the re-creation to be logged, got:\n%s", joined)
	}
	// A second timeout inside the back-off window is not re-created: the
	// plugin declines until the window passes, reported once.
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo/spin", "spin"), "echo"); tmpl != "" {
		t.Error("second spin should be interrupted and decline")
	}
	for range 2 {
		if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "" {
			t.Errorf("within the back-off window the closed instance must decline, got %q", tmpl)
		}
	}
	joined = strings.Join(logged, "\n")
	if n := strings.Count(joined, "instance closed (timeout or exit)"); n != 1 {
		t.Errorf("expected exactly one 'instance closed' line, got %d in:\n%s", n, joined)
	}
	if n := strings.Count(joined, "instance re-created"); n != 1 {
		t.Errorf("expected exactly one re-creation, got %d in:\n%s", n, joined)
	}
	// Once the window has passed it is re-created again.
	p.lock(waitForever)
	p.reinstantiatedAt = time.Now().Add(-reinstantiateBackoff)
	p.unlock()
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "page_content.html" {
		t.Errorf("after the back-off window the instance should be re-created, got %q", tmpl)
	}
	// Close is final and idempotent.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if tmpl, _ := p.RenderPage(hookCtx(settings, "/echo", ""), "echo"); tmpl != "" {
		t.Errorf("a closed plugin must not be re-created, got %q", tmpl)
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

// TestTemplateHooksDeclineWhileBusy: a template hook waits at most
// hookLockWait for a plugin whose instance is busy with another call, then
// declines (empty output, logged once) so one slow plugin does not stall
// every page render. Health reports the state for Admin → Plugins.
func TestTemplateHooksDeclineWhileBusy(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	p := loadEcho(t, Options{Logf: func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(f, a...))
	}})
	settings := map[string]string{"enabled": "true"}
	oldTimeout, oldWait := callTimeout, hookLockWait
	callTimeout, hookLockWait = 3*time.Second, 200*time.Millisecond
	t.Cleanup(func() { callTimeout, hookLockWait = oldTimeout, oldWait })
	if h := p.Health(); h != "ok" {
		t.Fatalf("idle plugin health = %q", h)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.RenderPage(hookCtx(settings, "/echo/spin", "spin"), "echo") // holds the instance until callTimeout
	}()
	time.Sleep(100 * time.Millisecond)
	if h := p.Health(); h != "busy" {
		t.Errorf("health while spinning = %q, want busy", h)
	}
	start := time.Now()
	head := p.TemplateHead(hookCtx(settings, "/", ""))
	data := p.TemplateData(hookCtx(settings, "/", ""))
	if head != "" || data != nil {
		t.Errorf("busy plugin should decline template hooks, got %q %v", head, data)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("template hooks waited %s; want about 2×hookLockWait", took)
	}
	<-done
	mu.Lock()
	busyLogs := 0
	for _, l := range logged {
		if strings.Contains(l, "busy") {
			busyLogs++
		}
	}
	mu.Unlock()
	if busyLogs != 1 {
		t.Errorf("expected exactly one busy log line, got %d:\n%s", busyLogs, strings.Join(logged, "\n"))
	}
	// The spin ran into its deadline: the instance is closed until a call
	// re-creates it, which template hooks still do once the lock is free.
	if h := p.Health(); h != "closed" {
		t.Errorf("health after the timeout = %q, want closed", h)
	}
	if head := p.TemplateHead(hookCtx(settings, "/", "")); head != "<!-- echo head -->" {
		t.Errorf("template_head after the instance was re-created = %q", head)
	}
	if h := p.Health(); h != "ok" {
		t.Errorf("health after re-creation = %q", h)
	}
}
