// Package guide holds goblog's builder documentation — how to write and
// publish plugins and themes — as markdown. It is not served by goblog:
// goblogplatform/goblog-plugin-docs vendors these files and renders them at
// /docs on goblog.live. The tests here keep the pages honest against the
// code (links resolve, every identifier they name exists).
package guide

import "embed"

// Content is every page, by file name.
//
//go:embed *.md
var Content embed.FS

// Page is one documentation page: its URL segment under /docs, the title
// (also its H1), and its file.
type Page struct {
	Slug, Title, File string
}

// Pages is the sidebar order. Slug "" is the index.
var Pages = []Page{
	{"", "Overview", "overview.md"},
	{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
	{"plugin-api", "Plugin API reference", "plugin-api.md"},
	{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
	{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
	{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
	{"directory-formats", "Directory formats", "directory-formats.md"},
}
