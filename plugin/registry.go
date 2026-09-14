package plugin

import (
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

// Registry manages all registered plugins.
type Registry struct {
	plugins []Plugin
	db      *gorm.DB
	mu      sync.RWMutex
	stopCh  chan struct{}
}

// NewRegistry creates a plugin registry.
func NewRegistry(db *gorm.DB) *Registry {
	return &Registry{
		db:     db,
		stopCh: make(chan struct{}),
	}
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
	r.plugins = append(r.plugins, p)
	log.Printf("Plugin registered: %s v%s", p.DisplayName(), p.Version())
}

// Plugins returns the list of registered plugins.
func (r *Registry) Plugins() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Plugin, len(r.plugins))
	copy(result, r.plugins)
	return result
}

// Init seeds plugin settings and calls OnInit for all plugins.
func (r *Registry) Init() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.db == nil {
		return nil
	}
	// Create the plugin_settings table if it doesn't exist
	r.db.AutoMigrate(&PluginSetting{})
	for _, p := range r.plugins {
		for _, s := range p.Settings() {
			setting := PluginSetting{
				PluginName: p.Name(),
				Key:        s.Key,
				Value:      s.DefaultValue,
			}
			r.db.Where("plugin_name = ? AND key = ?", p.Name(), s.Key).FirstOrCreate(&setting)
		}
		if err := p.OnInit(r.db); err != nil {
			log.Printf("Plugin %s init error: %v", p.Name(), err)
			return err
		}
	}
	return nil
}

// StartScheduledJobs launches goroutines for all plugin scheduled jobs.
func (r *Registry) StartScheduledJobs() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.plugins {
		for _, job := range p.ScheduledJobs() {
			go func(p Plugin, job ScheduledJob) {
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
					case <-r.stopCh:
						return
					}
				}
			}(p, job)
		}
	}
}

// Stop gracefully shuts down scheduled jobs.
func (r *Registry) Stop() {
	close(r.stopCh)
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
