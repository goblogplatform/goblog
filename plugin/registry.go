package plugin

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PluginSettingsGroup holds a plugin's setting definitions and current values
// for rendering in the admin settings page.
type PluginSettingsGroup struct {
	PluginName    string
	DisplayName   string
	Settings      []SettingDefinition
	CurrentValues map[string]string
}

// entry is one registered plugin with its lifecycle state.
type entry struct {
	plugin  Plugin
	path    string        // source file for dynamic plugins; "" for compiled-in
	stop    chan struct{} // closed by Unregister/Stop; ends this plugin's jobs
	stopped bool
	started bool // jobs running
}

// DynamicInfo describes a plugin loaded from a file at runtime.
type DynamicInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
	Path        string `json:"path"`
}

// Registry manages all registered plugins.
type Registry struct {
	entries []*entry
	plugins []Plugin // derived from entries; rebuilt on every change
	db      *gorm.DB
	mu      sync.RWMutex
}

// NewRegistry creates a plugin registry.
func NewRegistry(db *gorm.DB) *Registry {
	return &Registry{db: db}
}

// UpdateDb updates the database reference (used after wizard setup).
func (r *Registry) UpdateDb(db *gorm.DB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
}

// Register adds a compiled-in plugin to the registry.
func (r *Registry) Register(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(p, "")
}

// RegisterDynamic adds a plugin loaded from path at runtime. It fails when a
// plugin with the same Name() is already registered, since names key
// settings and pages.
func (r *Registry) RegisterDynamic(p Plugin, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findLocked(p.Name()) != nil {
		return fmt.Errorf("a plugin named %q is already registered", p.Name())
	}
	r.addLocked(p, path)
	return nil
}

func (r *Registry) addLocked(p Plugin, path string) {
	r.entries = append(r.entries, &entry{plugin: p, path: path, stop: make(chan struct{})})
	r.rebuildLocked()
	log.Printf("Plugin registered: %s v%s", p.DisplayName(), p.Version())
}

// Unregister removes a plugin and stops its scheduled jobs. Its settings are
// kept so a reinstall or update keeps the operator's configuration; use
// DeleteSettings to remove them. Yaegi cannot unload code, so a dynamic
// plugin's interpreter stays in memory until restart.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.plugin.Name() != name {
			continue
		}
		e.closeStopLocked()
		r.entries = append(append([]*entry(nil), r.entries[:i]...), r.entries[i+1:]...)
		r.rebuildLocked()
		log.Printf("Plugin unregistered: %s", name)
		return nil
	}
	return fmt.Errorf("plugin %q is not registered", name)
}

func (e *entry) closeStopLocked() {
	if !e.stopped {
		e.stopped = true
		close(e.stop)
	}
}

func (r *Registry) findLocked(name string) *entry {
	for _, e := range r.entries {
		if e.plugin.Name() == name {
			return e
		}
	}
	return nil
}

// rebuildLocked refreshes the derived plugins slice; existing readers keep
// iterating their own copy.
func (r *Registry) rebuildLocked() {
	plugins := make([]Plugin, len(r.entries))
	for i, e := range r.entries {
		plugins[i] = e.plugin
	}
	r.plugins = plugins
}

// Plugins returns the list of registered plugins.
func (r *Registry) Plugins() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Plugin, len(r.plugins))
	copy(result, r.plugins)
	return result
}

// Dynamic lists the plugins that were loaded from files at runtime.
func (r *Registry) Dynamic() []DynamicInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []DynamicInfo
	for _, e := range r.entries {
		if e.path != "" {
			out = append(out, DynamicInfo{Name: e.plugin.Name(), DisplayName: e.plugin.DisplayName(), Version: e.plugin.Version(), Path: e.path})
		}
	}
	return out
}

// Init seeds plugin settings and calls OnInit for every plugin. A plugin
// that fails does not stop the others; the failures are returned joined.
func (r *Registry) Init() error {
	r.mu.RLock()
	entries := append([]*entry(nil), r.entries...)
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return nil
	}
	// Create the plugin_settings and plugin_store tables if they don't exist
	db.AutoMigrate(&PluginSetting{}, &PluginStoreEntry{})
	var errs []error
	for _, e := range entries {
		if err := initPlugin(db, e.plugin); err != nil {
			log.Printf("Plugin %s init error: %v", e.plugin.Name(), err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func initPlugin(db *gorm.DB, p Plugin) error {
	for _, s := range p.Settings() {
		setting := PluginSetting{PluginName: p.Name(), Key: s.Key, Value: s.DefaultValue}
		db.Where("plugin_name = ? AND key = ?", p.Name(), s.Key).FirstOrCreate(&setting)
	}
	if err := p.OnInit(db); err != nil {
		return fmt.Errorf("plugin %s: %w", p.Name(), err)
	}
	return nil
}

// InitPlugin seeds settings, runs OnInit and starts the scheduled jobs of one
// plugin — what a hot install needs after RegisterDynamic.
func (r *Registry) InitPlugin(name string) error {
	r.mu.RLock()
	e := r.findLocked(name)
	db := r.db
	r.mu.RUnlock()
	if e == nil {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	if db == nil {
		return errors.New("database is not ready")
	}
	db.AutoMigrate(&PluginSetting{}, &PluginStoreEntry{})
	if err := initPlugin(db, e.plugin); err != nil {
		return err
	}
	r.startJobs(e)
	return nil
}

// StartScheduledJobs launches goroutines for all plugin scheduled jobs.
// Plugins whose jobs are already running are left alone.
func (r *Registry) StartScheduledJobs() {
	r.mu.RLock()
	entries := append([]*entry(nil), r.entries...)
	r.mu.RUnlock()
	for _, e := range entries {
		r.startJobs(e)
	}
}

func (r *Registry) startJobs(e *entry) {
	r.mu.Lock()
	if e.started || e.stopped {
		r.mu.Unlock()
		return
	}
	e.started = true
	r.mu.Unlock()
	for _, job := range e.plugin.ScheduledJobs() {
		go func(p Plugin, job ScheduledJob, stop <-chan struct{}) {
			ticker := time.NewTicker(job.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.mu.RLock()
					db := r.db
					r.mu.RUnlock()
					if db == nil {
						continue
					}
					settings := r.getPluginSettings(p.Name())
					if err := job.Run(db, settings); err != nil {
						log.Printf("Plugin %s job %s error: %v", p.Name(), job.Name, err)
					}
				case <-stop:
					return
				}
			}
		}(e.plugin, job, e.stop)
	}
}

// Stop gracefully shuts down every plugin's scheduled jobs.
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		e.closeStopLocked()
	}
}

// DeleteSettings removes a plugin's stored settings (uninstall).
func (r *Registry) DeleteSettings(name string) {
	r.mu.RLock()
	db := r.db
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.Where("plugin_name = ?", name).Delete(&PluginSetting{})
}

// getPluginSettings returns a plugin's settings as a simple key→value map.
func (r *Registry) getPluginSettings(pluginName string) map[string]string {
	if r.db == nil {
		return make(map[string]string)
	}
	var settings []PluginSetting
	r.db.Where("plugin_name = ?", pluginName).Find(&settings)
	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result
}

// InjectTemplateData gathers data from all plugins and merges it into
// the template data map. Adds "plugins", "plugin_head_html", and
// "plugin_footer_html" keys.
func (r *Registry) InjectTemplateData(c *gin.Context, templateName string, data gin.H) gin.H {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.db == nil {
		return data
	}

	pluginsData := gin.H{}
	headHTML := ""
	footerHTML := ""

	for _, p := range r.plugins {
		settings := r.getPluginSettings(p.Name())
		ctx := &HookContext{
			GinContext: c,
			DB:         r.db,
			Settings:   settings,
			Template:   templateName,
			Data:       data,
		}

		if pData := p.TemplateData(ctx); pData != nil {
			pluginsData[p.Name()] = pData
		}
		headHTML += p.TemplateHead(ctx)
		footerHTML += p.TemplateFooter(ctx)
	}

	data["plugins"] = pluginsData
	data["plugin_head_html"] = headHTML
	data["plugin_footer_html"] = footerHTML
	return data
}

// GetAllSettings returns all plugin setting definitions grouped by plugin.
func (r *Registry) GetAllSettings() []PluginSettingsGroup {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var groups []PluginSettingsGroup
	for _, p := range r.plugins {
		if defs := p.Settings(); len(defs) > 0 {
			values := r.getPluginSettings(p.Name())
			groups = append(groups, PluginSettingsGroup{
				PluginName:    p.Name(),
				DisplayName:   p.DisplayName(),
				Settings:      defs,
				CurrentValues: values,
			})
		}
	}
	return groups
}

// IsPluginEnabled checks if a plugin is enabled via its settings.
func (r *Registry) IsPluginEnabled(pluginName string) bool {
	settings := r.getPluginSettings(pluginName)
	return settings["enabled"] == "true"
}

// GetPagePlugin returns the plugin that owns a given page type, or nil.
func (r *Registry) GetPagePlugin(pageType string) Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.plugins {
		if !r.IsPluginEnabled(p.Name()) {
			continue
		}
		for _, page := range p.Pages() {
			if page.PageType == pageType {
				return p
			}
		}
	}
	return nil
}

// RenderPluginPage renders a plugin-owned page. subPath is the request path
// after the page slug ("" for the page itself). Returns the template name,
// its data, and whether the request was handled. A plugin handles a request
// either by returning a template name or by writing the response itself
// (for example JSON), in which case the template name is empty and the
// caller must not render anything.
func (r *Registry) RenderPluginPage(c *gin.Context, pageType, subPath string) (string, gin.H, bool) {
	p := r.GetPagePlugin(pageType)
	if p == nil {
		return "", nil, false
	}
	settings := r.getPluginSettings(p.Name())
	ctx := &HookContext{
		GinContext: c,
		DB:         r.db,
		Settings:   settings,
		Template:   pageType,
		SubPath:    subPath,
	}
	tmpl, data := p.RenderPage(ctx, pageType)
	if tmpl == "" {
		return "", nil, c.Writer.Written()
	}
	return tmpl, data, true
}

// GetNavItems returns navigation items from all enabled plugins that define pages.
func (r *Registry) GetNavItems() []PageDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var items []PageDefinition
	for _, p := range r.plugins {
		if !r.IsPluginEnabled(p.Name()) {
			continue
		}
		for _, page := range p.Pages() {
			if page.ShowInNav {
				items = append(items, page)
			}
		}
	}
	return items
}

// IsPageTypeEnabled returns true if the plugin that owns the given page type is enabled.
func (r *Registry) IsPageTypeEnabled(pageType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.plugins {
		for _, page := range p.Pages() {
			if page.PageType == pageType {
				return r.IsPluginEnabled(p.Name())
			}
		}
	}
	return false
}

// HasPageType returns true if any registered plugin (enabled or not) defines the given page type.
func (r *Registry) HasPageType(pageType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.plugins {
		for _, page := range p.Pages() {
			if page.PageType == pageType {
				return true
			}
		}
	}
	return false
}

// UpdateSetting saves a single plugin setting.
func (r *Registry) UpdateSetting(pluginName, key, value string) {
	r.db.Where("plugin_name = ? AND key = ?", pluginName, key).
		Assign(PluginSetting{Value: value}).
		FirstOrCreate(&PluginSetting{PluginName: pluginName, Key: key, Value: value})
}

// Middleware returns a Gin middleware that stores the registry on the context.
func Middleware(registry *Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("plugin_registry", registry)
		c.Next()
	}
}
