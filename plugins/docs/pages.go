package docs

// page is one documentation page: its URL segment under /docs, the sidebar
// title (also the page's H1), and its file in content/.
type page struct {
	Slug, Title, File string
}

// pages is the sidebar, in order. Slug "" is the index.
var pages = []page{
	{"", "Overview", "overview.md"},
	{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
	{"plugin-api", "Plugin API reference", "plugin-api.md"},
	{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
	{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
	{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
	{"directory-formats", "Directory formats", "directory-formats.md"},
}
