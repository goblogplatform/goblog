// Package wasm runs WebAssembly plugins (Extism) behind the plugin.Plugin
// interface. Each hook becomes a JSON call of the matching export; plugins
// get persistent storage through store_* host functions and HTTP through
// Extism's built-in client, limited to their allowed hosts.
package wasm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"goblog/plugin"

	extism "github.com/extism/go-sdk"
	"github.com/gin-gonic/gin"
	"github.com/tetratelabs/wazero"
	"gorm.io/gorm"
)

// Options configure a loaded plugin.
type Options struct {
	Store        plugin.Store                     // nil → writes fail, reads are empty
	AllowedHosts []string                         // hosts the plugin may reach over HTTP; nil → none
	Logf         func(format string, args ...any) // nil → log.Printf
	// NoCache skips the shared compilation cache — for one-off loads such
	// as validation, so the compiled code is freed on Close.
	NoCache bool
}

var (
	callTimeout  = 10 * time.Second  // template_* and render_page
	jobTimeout   = 120 * time.Second // run_job and on_init
	hookLockWait = 2 * time.Second   // how long template_* hooks wait for a busy instance
)

// waitForever makes call wait for the instance without bound.
const waitForever time.Duration = -1

const (
	memoryPages          = 1024    // 64 MB
	maxHTTPResponseBytes = 8 << 20 // 8 MB
)

// ErrNoIdentity is returned when a module has no identity export.
var ErrNoIdentity = errors.New("wasm plugin: missing identity export")

// Exports are the functions a plugin module may export; goblog calls the
// ones present. The documentation is linted against this list.
var Exports = []string{"identity", "settings", "pages", "jobs", "template_head", "template_footer", "template_data", "render_page", "run_job", "on_init"}

// HostFunctions are what goblog offers in the extism:host/user namespace
// besides the PDK's own logging and http_request.
var HostFunctions = []string{"store_get", "store_set", "store_delete", "store_list"}

// compilationCache is shared by every instance so a module is compiled once
// per process (validate, install and boot all load the same bytes).
var compilationCache = wazero.NewCompilationCache()

func init() {
	// The SDK's log level is process-wide and defaults to Off, which would
	// silently drop everything plugins log. Info and above reach Logf.
	extism.SetLogLevel(extism.LogLevelInfo)
}

// reinstantiateBackoff is the minimum gap between two re-creations of a
// plugin's instance after a timeout closed it (see call).
const reinstantiateBackoff = 30 * time.Second

// Plugin is a loaded WebAssembly plugin. It implements plugin.Plugin.
type Plugin struct {
	sem               chan struct{} // one-slot semaphore serialising calls; a timed acquire is what a mutex cannot give
	busyLogged        atomic.Bool   // "instance busy" already logged for the current busy stretch
	ext               *extism.Plugin
	data              []byte // module bytes, kept so a closed instance can be re-created
	opts              Options
	name              string
	identity          Identity
	settings          []plugin.SettingDefinition
	pages             []plugin.PageDefinition
	jobs              []jobDef
	exports           map[string]bool // export presence, read once at load
	hasEnabledSetting bool
	closed            bool      // Close() was called (under mu)
	closedLogged      bool      // "instance closed" warning already emitted (under mu)
	instanceClosed    bool      // the wazero module is closed (timeout/exit); re-create before use (under mu)
	closedByDeadline  bool      // instanceClosed was caused by the last call's own deadline (under mu)
	reinstantiatedAt  time.Time // last time call re-created the instance (under mu)
}

// Load reads a .wasm file and instantiates it.
func Load(path string, opts Options) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := LoadBytes(data, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// LoadBytes instantiates a plugin from module bytes and reads its identity,
// settings, pages and jobs once.
func LoadBytes(data []byte, opts Options) (*Plugin, error) {
	if opts.Store == nil {
		opts.Store = nopStore{}
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	p := &Plugin{opts: opts, data: data, sem: make(chan struct{}, 1)}
	ext, err := p.instantiate()
	if err != nil {
		return nil, err
	}
	p.ext = ext
	p.exports = map[string]bool{}
	for _, name := range Exports {
		p.exports[name] = ext.FunctionExists(name)
	}
	if !p.exports["identity"] {
		ext.Close(context.Background())
		return nil, ErrNoIdentity
	}
	if err := p.callJSON("identity", nil, &p.identity, callTimeout, waitForever); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin: identity: %w", err)
	}
	if p.identity.Name == "" || p.identity.Version == "" {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin: identity must include name and version")
	}
	p.name = p.identity.Name

	var defs []settingDef
	if err := p.optionalJSON("settings", &defs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: settings: %w", p.name, err)
	}
	for _, d := range defs {
		if d.Key == "enabled" {
			p.hasEnabledSetting = true
		}
		p.settings = append(p.settings, plugin.SettingDefinition{Key: d.Key, Type: d.Type, DefaultValue: d.Default, Label: d.Label, Description: d.Description})
	}
	var pgs []pageDef
	if err := p.optionalJSON("pages", &pgs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: pages: %w", p.name, err)
	}
	for _, d := range pgs {
		p.pages = append(p.pages, plugin.PageDefinition{PageType: d.PageType, Title: d.Title, Slug: d.Slug, ShowInNav: d.ShowInNav, NavOrder: d.NavOrder, Description: d.Description})
	}
	if err := p.optionalJSON("jobs", &p.jobs); err != nil {
		ext.Close(context.Background())
		return nil, fmt.Errorf("wasm plugin %s: jobs: %w", p.name, err)
	}
	return p, nil
}

// instantiate builds a fresh Extism instance from the module bytes with the
// plugin's manifest, config and host functions. Used at load and again by
// call when a timeout has closed the running instance.
func (p *Plugin) instantiate() (*extism.Plugin, error) {
	manifest := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: p.data}},
		AllowedHosts: p.opts.AllowedHosts,
		Memory:       &extism.ManifestMemory{MaxPages: memoryPages, MaxHttpResponseBytes: maxHTTPResponseBytes},
		// Non-zero so the runtime is built with close-on-context-done; the
		// effective timeout is set per call (see call).
		Timeout: uint64(callTimeout / time.Millisecond),
	}
	// The SDK layers close-on-context-done and the memory limit on top. The
	// shared cache keeps a module's compiled code resident for the life of
	// the process; NoCache callers (validation of a public submission) skip
	// it so the compiled code is freed when Close runs instead of pinning
	// memory for a module that may never be installed.
	runtimeConfig := wazero.NewRuntimeConfig()
	if !p.opts.NoCache {
		runtimeConfig = runtimeConfig.WithCompilationCache(compilationCache)
	}
	config := extism.PluginConfig{
		EnableWasi:    true,
		RuntimeConfig: runtimeConfig,
		// Real clock, sleep and randomness: wazero's defaults are a fake
		// clock starting in 2022, a no-op sleep and a fixed-seed RNG.
		ModuleConfig: wazero.NewModuleConfig().WithSysWalltime().WithSysNanotime().WithSysNanosleep().WithRandSource(rand.Reader),
	}
	ext, err := extism.NewPlugin(context.Background(), manifest, config, hostFunctions(p))
	if err != nil {
		return nil, fmt.Errorf("wasm plugin: %w", err)
	}
	ext.SetLogger(func(level extism.LogLevel, msg string) {
		p.opts.Logf("plugin %s: %s: %s", p.displayForLog(), level, msg)
	})
	return ext, nil
}

func (p *Plugin) displayForLog() string {
	if p.name != "" {
		return p.name
	}
	return "(loading)"
}

// errClosed is returned by call after Close.
var errClosed = errors.New("wasm plugin: instance closed")

// errBusy is returned by call when the instance stayed busy with another
// call for the whole bounded wait.
var errBusy = errors.New("wasm plugin: instance busy")

// lock acquires the instance, waiting at most wait (waitForever: no bound).
func (p *Plugin) lock(wait time.Duration) bool {
	if wait < 0 {
		p.sem <- struct{}{}
		return true
	}
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	if wait == 0 {
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (p *Plugin) unlock() { <-p.sem }

// Health reports the instance state for the admin page: "busy" while
// another call holds it, "closed" when it was closed (timeout or exit) and
// not re-created yet, otherwise "ok".
func (p *Plugin) Health() string {
	if !p.lock(0) {
		return "busy"
	}
	defer p.unlock()
	if p.closed || p.instanceClosed {
		return "closed"
	}
	return "ok"
}

// Close releases the instance. Calling it again is a no-op.
func (p *Plugin) Close() error {
	p.lock(waitForever)
	defer p.unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.ext.Close(context.Background())
}

// Identity returns what the plugin reported.
func (p *Plugin) Identity() Identity { return p.identity }

// AllowedHosts returns the hosts this instance may reach.
func (p *Plugin) AllowedHosts() []string { return p.opts.AllowedHosts }

// call runs an export under the instance lock with the given timeout,
// waiting at most wait for the lock (waitForever: unbounded). A non-zero
// exit or a runtime error is returned as error; output is the raw bytes.
//
// The timeout has one mechanism: ext.Timeout, which CallWithContext turns
// into a context deadline that wazero (built with close-on-context-done)
// enforces by closing the module. After that every later call fails with
// "module is closed". The first such call re-creates the instance from the
// module bytes and retries once, so one slow request does not take the
// plugin down for good; a plugin that keeps timing out is re-created at
// most once per reinstantiateBackoff and declines in between, reported
// once per closed instance.
func (p *Plugin) call(name string, input []byte, timeout, wait time.Duration) ([]byte, error) {
	if !p.lock(wait) {
		if p.busyLogged.CompareAndSwap(false, true) {
			p.opts.Logf("plugin %s: instance busy for over %s; %s declined until it is free", p.displayForLog(), wait, name)
		}
		return nil, errBusy
	}
	defer p.unlock()
	p.busyLogged.Store(false)
	if p.closed {
		return nil, errClosed
	}
	if p.instanceClosed && !p.reviveLocked() {
		return nil, errClosed
	}
	out, err := p.runLocked(name, input, timeout)
	if err == nil || !p.instanceClosed || p.closedByDeadline {
		// Success, an ordinary plugin error, or this very call closed the
		// instance by running into its deadline: the next call re-creates it
		// (retrying a call that just timed out would only time out again).
		return out, err
	}
	// The instance was already closed before this call ran (guest exit or a
	// closure the previous call did not observe): re-create and retry once.
	if !p.reviveLocked() {
		return nil, errClosed
	}
	return p.runLocked(name, input, timeout)
}

// runLocked calls an export and records, structurally rather than by error
// text, whether the instance is now closed: wazero closes the module when the
// call runs into its deadline (close-on-context-done) or the guest exits.
// The error wording is only a secondary hint, so a runtime wording change
// cannot strand the plugin.
func (p *Plugin) runLocked(name string, input []byte, timeout time.Duration) ([]byte, error) {
	start := time.Now()
	out, err := p.callLocked(name, input, timeout)
	if err == nil {
		return out, nil
	}
	p.closedByDeadline = time.Since(start) >= timeout
	if p.closedByDeadline || strings.Contains(strings.ToLower(err.Error()), "closed") {
		p.instanceClosed = true
	}
	return nil, err
}

// reviveLocked re-creates a closed instance, honouring the back-off; when it
// cannot, the closed state is reported once.
func (p *Plugin) reviveLocked() bool {
	if p.reinstantiateLocked() {
		p.instanceClosed = false
		p.closedByDeadline = false
		return true
	}
	if !p.closedLogged {
		p.closedLogged = true
		p.opts.Logf("plugin %s: instance closed (timeout or exit); it is re-created at most once per %s", p.displayForLog(), reinstantiateBackoff)
	}
	return false
}

func (p *Plugin) callLocked(name string, input []byte, timeout time.Duration) ([]byte, error) {
	p.ext.Timeout = timeout
	code, out, err := p.ext.CallWithContext(context.Background(), name, input)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s returned %d: %s", name, code, p.ext.GetError())
	}
	return out, nil
}

// reinstantiateLocked replaces a closed instance with a fresh one unless one
// was already re-created within reinstantiateBackoff. Reports whether the
// caller may retry.
func (p *Plugin) reinstantiateLocked() bool {
	if !p.reinstantiatedAt.IsZero() && time.Since(p.reinstantiatedAt) < reinstantiateBackoff {
		return false
	}
	// Stamped on the attempt, not the outcome, so a failing instantiate()
	// is not retried on every call either.
	p.reinstantiatedAt = time.Now()
	ext, err := p.instantiate()
	if err != nil {
		p.opts.Logf("plugin %s: re-creating the instance failed: %v", p.displayForLog(), err)
		return false
	}
	p.ext.Close(context.Background()) // releases the compiled module and runtime
	p.ext = ext
	p.closedLogged = false
	p.opts.Logf("plugin %s: instance re-created after it was closed", p.displayForLog())
	return true
}

func (p *Plugin) callJSON(name string, input any, out any, timeout, wait time.Duration) error {
	var in []byte
	if input != nil {
		var err error
		if in, err = json.Marshal(input); err != nil {
			return err
		}
	}
	b, err := p.call(name, in, timeout, wait)
	if err != nil {
		return err
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: invalid JSON output: %w", name, err)
	}
	return nil
}

// optionalJSON is callJSON for load-time exports that may be absent.
func (p *Plugin) optionalJSON(name string, out any) error {
	if !p.exports[name] {
		return nil
	}
	return p.callJSON(name, nil, out, callTimeout, waitForever)
}

func (p *Plugin) enabled(settings map[string]string) bool {
	return !p.hasEnabledSetting || settings["enabled"] == "true"
}

func makeCtx(ctx *plugin.HookContext) hookInput {
	hc := hookInput{Settings: ctx.Settings, Template: ctx.Template}
	if hc.Settings == nil {
		hc.Settings = map[string]string{}
	}
	hc.Request = requestCtx{SubPath: ctx.SubPath, Query: map[string]string{}}
	if ctx.GinContext != nil && ctx.GinContext.Request != nil {
		r := ctx.GinContext.Request
		hc.Request.Path = r.URL.Path
		hc.Request.Method = r.Method
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				hc.Request.Query[k] = v[0]
			}
		}
	}
	return hc
}

// --- plugin.Plugin ---

func (p *Plugin) Name() string                         { return p.identity.Name }
func (p *Plugin) DisplayName() string                  { return p.identity.DisplayName }
func (p *Plugin) Version() string                      { return p.identity.Version }
func (p *Plugin) Settings() []plugin.SettingDefinition { return p.settings }
func (p *Plugin) Pages() []plugin.PageDefinition       { return p.pages }

func (p *Plugin) ScheduledJobs() []plugin.ScheduledJob {
	jobs := make([]plugin.ScheduledJob, 0, len(p.jobs))
	for _, j := range p.jobs {
		name := j.Name
		interval := time.Duration(j.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Hour
		}
		jobs = append(jobs, plugin.ScheduledJob{Name: name, Interval: interval, Run: func(_ *gorm.DB, settings map[string]string) error {
			if !p.enabled(settings) {
				return nil
			}
			if err := p.callJSON("run_job", jobInput{Name: name, Settings: settings}, nil, jobTimeout, waitForever); err != nil {
				return fmt.Errorf("wasm plugin %s job %s: %w", p.name, name, err)
			}
			return nil
		}})
	}
	return jobs
}

func (p *Plugin) stringHook(name string, ctx *plugin.HookContext) string {
	if !p.enabled(ctx.Settings) || !p.exports[name] {
		return ""
	}
	in, _ := json.Marshal(makeCtx(ctx))
	out, err := p.call(name, in, callTimeout, hookLockWait)
	if err != nil {
		if !errors.Is(err, errBusy) { // busy is reported once by call
			p.opts.Logf("plugin %s: %s: %v", p.name, name, err)
		}
		return ""
	}
	return string(out)
}

func (p *Plugin) TemplateHead(ctx *plugin.HookContext) string {
	return p.stringHook("template_head", ctx)
}
func (p *Plugin) TemplateFooter(ctx *plugin.HookContext) string {
	return p.stringHook("template_footer", ctx)
}

func (p *Plugin) TemplateData(ctx *plugin.HookContext) gin.H {
	if !p.enabled(ctx.Settings) || !p.exports["template_data"] {
		return nil
	}
	var data map[string]any
	if err := p.callJSON("template_data", makeCtx(ctx), &data, callTimeout, hookLockWait); err != nil {
		if !errors.Is(err, errBusy) {
			p.opts.Logf("plugin %s: template_data: %v", p.name, err)
		}
		return nil
	}
	if data == nil {
		return nil
	}
	return gin.H(data)
}

// OnInit calls on_init with the plugin's current settings: the declared
// defaults overlaid with whatever is stored for it (see initInput).
func (p *Plugin) OnInit(db *gorm.DB) error {
	if !p.exports["on_init"] {
		return nil
	}
	settings := map[string]string{}
	for _, s := range p.settings {
		settings[s.Key] = s.DefaultValue
	}
	if db != nil {
		var stored []plugin.PluginSetting
		if err := db.Where("plugin_name = ?", p.name).Find(&stored).Error; err != nil {
			return fmt.Errorf("wasm plugin %s: on_init: load settings: %w", p.name, err)
		}
		for _, s := range stored {
			settings[s.Key] = s.Value
		}
	}
	if err := p.callJSON("on_init", initInput{Settings: settings}, nil, jobTimeout, waitForever); err != nil {
		return fmt.Errorf("wasm plugin %s: on_init: %w", p.name, err)
	}
	return nil
}

// RenderPage maps the render_page result onto goblog's page rendering:
// html → page_content.html; template+data → passed through; raw → written
// directly. Errors and unknown shapes decline the request (404 upstream).
func (p *Plugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	owned := false
	for _, pg := range p.pages {
		if pg.PageType == pageType {
			owned = true
		}
	}
	if !owned || !p.exports["render_page"] {
		return "", nil
	}
	var res renderResult
	if err := p.callJSON("render_page", makeCtx(ctx), &res, callTimeout, waitForever); err != nil {
		p.opts.Logf("plugin %s: render_page %q: %v", p.name, ctx.SubPath, err)
		return "", nil
	}
	switch {
	case res.Raw != nil:
		if ctx.GinContext == nil {
			return "", nil
		}
		status := res.Raw.Status
		if status == 0 {
			status = 200
		}
		if status < 100 || status > 599 {
			// net/http panics on codes outside this range.
			p.opts.Logf("plugin %s: render_page returned invalid status %d", p.name, status)
			return "", nil
		}
		ct := res.Raw.ContentType
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		ctx.GinContext.Data(status, ct, []byte(res.Raw.Body))
		return "", nil
	case res.Template != "":
		data := gin.H{}
		for k, v := range res.Data {
			data[k] = v
		}
		return res.Template, data
	case res.HTML != nil:
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": *res.HTML}
	}
	return "", nil
}
