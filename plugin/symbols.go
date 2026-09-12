package plugin

import (
	"reflect"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Symbols exports the plugin package types to the Yaegi interpreter so
// dynamic plugins can `import "goblog/plugin"` and use plugin.BasePlugin,
// plugin.HookContext, etc. Yaegi keys symbol maps as
// "<import path>/<package name>", the same way its stdlib does ("fmt/fmt").
//
// The _Plugin entry and the _goblog_plugin_Plugin wrapper below are what let
// an interpreted type satisfy the compiled Plugin interface; they follow the
// shape `yaegi extract` generates. Registry internals are deliberately not
// exported: a plugin only needs the types it implements or receives.
var Symbols = map[string]map[string]reflect.Value{
	"goblog/plugin/plugin": {
		"Plugin":            reflect.ValueOf((*Plugin)(nil)),
		"BasePlugin":        reflect.ValueOf((*BasePlugin)(nil)),
		"HookContext":       reflect.ValueOf((*HookContext)(nil)),
		"SettingDefinition": reflect.ValueOf((*SettingDefinition)(nil)),
		"ScheduledJob":      reflect.ValueOf((*ScheduledJob)(nil)),
		"PageDefinition":    reflect.ValueOf((*PageDefinition)(nil)),

		// interface wrapper definitions
		"_Plugin": reflect.ValueOf((*_goblog_plugin_Plugin)(nil)),
	},
}

// _goblog_plugin_Plugin is an interface wrapper for Plugin type
type _goblog_plugin_Plugin struct {
	IValue          interface{}
	WDisplayName    func() string
	WName           func() string
	WOnInit         func(db *gorm.DB) error
	WPages          func() []PageDefinition
	WRenderPage     func(ctx *HookContext, pageType string) (templateName string, data gin.H)
	WScheduledJobs  func() []ScheduledJob
	WSettings       func() []SettingDefinition
	WTemplateData   func(ctx *HookContext) gin.H
	WTemplateFooter func(ctx *HookContext) string
	WTemplateHead   func(ctx *HookContext) string
	WVersion        func() string
}

func (W _goblog_plugin_Plugin) DisplayName() string {
	return W.WDisplayName()
}
func (W _goblog_plugin_Plugin) Name() string {
	return W.WName()
}
func (W _goblog_plugin_Plugin) OnInit(db *gorm.DB) error {
	return W.WOnInit(db)
}
func (W _goblog_plugin_Plugin) Pages() []PageDefinition {
	return W.WPages()
}
func (W _goblog_plugin_Plugin) RenderPage(ctx *HookContext, pageType string) (templateName string, data gin.H) {
	return W.WRenderPage(ctx, pageType)
}
func (W _goblog_plugin_Plugin) ScheduledJobs() []ScheduledJob {
	return W.WScheduledJobs()
}
func (W _goblog_plugin_Plugin) Settings() []SettingDefinition {
	return W.WSettings()
}
func (W _goblog_plugin_Plugin) TemplateData(ctx *HookContext) gin.H {
	return W.WTemplateData(ctx)
}
func (W _goblog_plugin_Plugin) TemplateFooter(ctx *HookContext) string {
	return W.WTemplateFooter(ctx)
}
func (W _goblog_plugin_Plugin) TemplateHead(ctx *HookContext) string {
	return W.WTemplateHead(ctx)
}
func (W _goblog_plugin_Plugin) Version() string {
	return W.WVersion()
}
