// Package wasm runs WebAssembly plugins (Extism) behind the plugin.Plugin
// interface. Each hook becomes a JSON call of the matching export; plugins
// get persistent storage through store_* host functions and HTTP through
// Extism's built-in client, limited to their allowed hosts.
package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"goblog/plugin"

	extism "github.com/extism/go-sdk"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Options configure a loaded plugin.
type Options struct {
	Store        plugin.Store                     // nil → writes fail, reads are empty
	AllowedHosts []string                         // hosts the plugin may reach over HTTP; nil → none
	Logf         func(format string, args ...any) // nil → log.Printf
}

var (
	callTimeout = 10 * time.Second  // template_* and render_page
	jobTimeout  = 120 * time.Second // run_job and on_init
)

const (
	memoryPages          = 1024    // 64 MB
	maxHTTPResponseBytes = 8 << 20 // 8 MB
)

// ErrNoIdentity is returned when a module has no identity export.
var ErrNoIdentity = errors.New("wasm plugin: missing identity export")

func init() {
	// The SDK's log level is process-wide and defaults to Off, which would
	// silently drop everything plugins log. Info and above reach Logf.
	extism.SetLogLevel(extism.LogLevelInfo)
}

// Plugin is a loaded WebAssembly plugin. It implements plugin.Plugin.
type Plugin struct {
	mu                sync.Mutex
	ext               *extism.Plugin
	opts              Options
	name              string
	identity          Identity
	settings          []plugin.SettingDefinition
	pages             []plugin.PageDefinition
	jobs              []jobDef
	hasEnabledSetting bool
	closedLogged      bool // "instance closed" warning already emitted (under mu)
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
	p := &Plugin{opts: opts}
	manifest := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: data}},
		AllowedHosts: opts.AllowedHosts,
		Memory:       &extism.ManifestMemory{MaxPages: memoryPages, MaxHttpResponseBytes: maxHTTPResponseBytes},
		// Non-zero so the runtime is built with close-on-context-done; the
		// effective timeout is set per call.
		Timeout: uint64(callTimeout / time.Millisecond),
	}
	ext, err := extism.NewPlugin(context.Background(), manifest, extism.PluginConfig{EnableWasi: true}, hostFunctions(p))
	if err != nil {
		return nil, fmt.Errorf("wasm plugin: %w", err)
	}
	p.ext = ext
	ext.SetLogger(func(level extism.LogLevel, msg string) {
		p.opts.Logf("plugin %s: %s: %s", p.displayForLog(), level, msg)
	})
	if !ext.FunctionExists("identity") {
		ext.Close(context.Background())
		return nil, ErrNoIdentity
	}
	if err := p.callJSON("identity", nil, &p.identity, callTimeout); err != nil {
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

func (p *Plugin) displayForLog() string {
	if p.name != "" {
		return p.name
	}
	return "(loading)"
}

// Close releases the instance.
func (p *Plugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ext.Close(context.Background())
}

// Identity returns what the plugin reported.
func (p *Plugin) Identity() Identity { return p.identity }

// AllowedHosts returns the hosts this instance may reach.
func (p *Plugin) AllowedHosts() []string { return p.opts.AllowedHosts }

// call runs an export under the mutex with the given timeout. A non-zero
// exit or a runtime error is returned as error; output is the raw bytes.
//
// After a timeout wazero closes the module for good: every later call fails
// with "module is closed". That is reported once, clearly; the caller keeps
// declining until the plugin is reinstalled or goblog restarts.
func (p *Plugin) call(name string, input []byte, timeout time.Duration) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ext.Timeout = timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	code, out, err := p.ext.CallWithContext(ctx, name, input)
	if err != nil {
		if err.Error() == "module is closed" && !p.closedLogged {
			p.closedLogged = true
			p.opts.Logf("plugin %s: instance closed after a timeout; reinstall or restart to recover", p.displayForLog())
		}
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s returned %d: %s", name, code, p.ext.GetError())
	}
	return out, nil
}

func (p *Plugin) callJSON(name string, input any, out any, timeout time.Duration) error {
	var in []byte
	if input != nil {
		var err error
		if in, err = json.Marshal(input); err != nil {
			return err
		}
	}
	b, err := p.call(name, in, timeout)
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
	if !p.ext.FunctionExists(name) {
		return nil
	}
	return p.callJSON(name, nil, out, callTimeout)
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
			if err := p.callJSON("run_job", jobInput{Name: name, Settings: settings}, nil, jobTimeout); err != nil {
				return fmt.Errorf("wasm plugin %s job %s: %w", p.name, name, err)
			}
			return nil
		}})
	}
	return jobs
}

func (p *Plugin) stringHook(name string, ctx *plugin.HookContext) string {
	if !p.enabled(ctx.Settings) || !p.ext.FunctionExists(name) {
		return ""
	}
	in, _ := json.Marshal(makeCtx(ctx))
	out, err := p.call(name, in, callTimeout)
	if err != nil {
		p.opts.Logf("plugin %s: %s: %v", p.name, name, err)
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
	if !p.enabled(ctx.Settings) || !p.ext.FunctionExists("template_data") {
		return nil
	}
	var data map[string]any
	if err := p.callJSON("template_data", makeCtx(ctx), &data, callTimeout); err != nil {
		p.opts.Logf("plugin %s: template_data: %v", p.name, err)
		return nil
	}
	if data == nil {
		return nil
	}
	return gin.H(data)
}

// OnInit calls on_init with the plugin's default settings (see initInput).
func (p *Plugin) OnInit(_ *gorm.DB) error {
	if !p.ext.FunctionExists("on_init") {
		return nil
	}
	settings := map[string]string{}
	for _, s := range p.settings {
		settings[s.Key] = s.DefaultValue
	}
	if err := p.callJSON("on_init", initInput{Settings: settings}, nil, jobTimeout); err != nil {
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
	if !owned || !p.ext.FunctionExists("render_page") {
		return "", nil
	}
	var res renderResult
	if err := p.callJSON("render_page", makeCtx(ctx), &res, callTimeout); err != nil {
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
