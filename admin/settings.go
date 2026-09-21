package admin

import (
	"sort"

	"goblog/blog"
)

// SettingField is one site setting as the Settings page renders it: the
// stored row plus the label and help text shown next to its input.
type SettingField struct {
	blog.Setting
	Label string
	Help  string
}

// SettingGroup is one tab on the Settings page.
type SettingGroup struct {
	ID     string // tab id, also the location.hash fragment that reopens it
	Title  string
	Fields []SettingField
}

// settingMeta is the label and help for one known setting key.
type settingMeta struct {
	Key   string
	Label string
	Help  string
}

// settingGroupDef is one group of known setting keys, in display order.
type settingGroupDef struct {
	ID       string
	Title    string
	Settings []settingMeta
}

// advancedGroupID is the tab that collects every setting key the table
// below does not know about, labelled by its raw key, so a setting a plugin
// or a later migration adds is always reachable from the UI.
const advancedGroupID = "advanced"

// settingGroups is the Settings page layout: tabs in order, and the keys
// each one shows in order. A key seeded in tools/migrate.go that is missing
// here still renders, under Advanced.
var settingGroups = []settingGroupDef{
	{ID: "site", Title: "Site", Settings: []settingMeta{
		{Key: "site_title", Label: "Site title", Help: "Shown in the header and browser tab."},
		{Key: "site_subtitle", Label: "Subtitle", Help: "Tagline shown under the title."},
		{Key: "site_logo_letters", Label: "Logo letters", Help: "Short initials used as the logo."},
		{Key: "site_tags", Label: "Site tags", Help: "Comma-separated keywords for the site's meta tags."},
		{Key: "site_url", Label: "Site URL", Help: "Public address of this site, used for links in feeds, emails and the sitemap."},
		{Key: "robots_tag", Label: "Robots tag", Help: "Value of the robots meta tag, for example \"index, follow\"."},
	}},
	{ID: "appearance", Title: "Appearance", Settings: []settingMeta{
		{Key: "theme", Label: "Theme", Help: "Browse, install and preview themes under Themes."},
		{Key: "landing_page_image", Label: "Landing page image", Help: "Picture shown on the landing page."},
		{Key: "favicon", Label: "Favicon", Help: "Icon shown in the browser tab."},
		{Key: "custom_header_code", Label: "Custom header code", Help: "HTML added to the <head> of every page."},
		{Key: "custom_footer_code", Label: "Custom footer code", Help: "HTML added before </body> on every page."},
	}},
	{ID: "comments", Title: "Comments", Settings: []settingMeta{
		{Key: "comments_require_login", Label: "Require login to comment", Help: "When off, anyone can leave a comment."},
	}},
	{ID: "directories", Title: "Directories", Settings: []settingMeta{
		{Key: "plugin_directory_url", Label: "Plugin directory URL", Help: "Index the Plugins page installs from."},
		{Key: "theme_directory_url", Label: "Theme directory URL", Help: "Index the Themes page installs from."},
	}},
}

// GroupSettings arranges the site settings into the Settings page's tabs.
// Groups with no stored setting are left out; keys the layout does not
// know land in an Advanced tab, sorted, labelled by their raw key.
func GroupSettings(settings map[string]blog.Setting) []SettingGroup {
	seen := make(map[string]bool, len(settings))
	var groups []SettingGroup
	for _, def := range settingGroups {
		g := SettingGroup{ID: def.ID, Title: def.Title}
		for _, m := range def.Settings {
			s, ok := settings[m.Key]
			if !ok {
				continue
			}
			seen[m.Key] = true
			g.Fields = append(g.Fields, SettingField{Setting: s, Label: m.Label, Help: m.Help})
		}
		if len(g.Fields) > 0 {
			groups = append(groups, g)
		}
	}

	var rest []string
	for key := range settings {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	if len(rest) == 0 {
		return groups
	}
	sort.Strings(rest)
	adv := SettingGroup{ID: advancedGroupID, Title: "Advanced"}
	for _, key := range rest {
		adv.Fields = append(adv.Fields, SettingField{Setting: settings[key], Label: key})
	}
	return append(groups, adv)
}
