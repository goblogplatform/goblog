package docs

import (
	"regexp"
	"strings"
	"unicode"

	gplugin "goblog/plugin"
)

// snippetLen is roughly how many characters of text a search result shows
// around the first match.
const snippetLen = 160

var (
	reMarkdownNoise = regexp.MustCompile("(?m)^#+ |[`*>]|\\[([^\\]]*)\\]\\([^)]*\\)")
	reSpace         = regexp.MustCompile(`\s+`)
)

// searchText reduces markdown source to searchable text: heading markers,
// emphasis, code ticks and link targets removed, whitespace collapsed.
// Underscores stay: they are identifiers (allowed_hosts) far more often
// than emphasis in these pages.
func searchText(src []byte) string {
	s := reMarkdownNoise.ReplaceAllString(string(src), "$1")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// snippet is about snippetLen characters of text around the first
// case-insensitive occurrence of q, cut at word boundaries and marked with
// ellipses where text was left out. With no occurrence it is the start of
// the text.
func snippet(text, q string) string {
	at := strings.Index(strings.ToLower(text), strings.ToLower(q))
	if at < 0 {
		at = 0
	}
	start := at - snippetLen/3
	if start < 0 {
		start = 0
	}
	end := start + snippetLen
	if end > len(text) {
		end = len(text)
	}
	// Move to word boundaries so the snippet never starts or ends mid-word
	// (or mid-rune).
	for start > 0 && !unicode.IsSpace(rune(text[start-1])) {
		start--
	}
	for end < len(text) && !unicode.IsSpace(rune(text[end])) {
		end++
	}
	out := text[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

// Search answers the site search (plugin.Searcher) with every page whose
// title or text contains the query — title matches first, then text
// matches, each in sidebar order — linking under the docs page's current
// slug.
func (p *Plugin) Search(ctx *gplugin.HookContext, query string) []gplugin.SearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	def := p.Pages()[0]
	base := "/" + gplugin.PageSlug(ctx.DB, def.PageType, def.Slug)
	var byTitle, byText []gplugin.SearchResult
	for _, pg := range pages {
		r := p.bySlug[pg.Slug]
		url := base
		if pg.Slug != "" {
			url += "/" + pg.Slug
		}
		hit := gplugin.SearchResult{Title: r.Title, URL: url, Summary: snippet(r.Text, q), Kind: "Docs"}
		switch {
		case strings.Contains(strings.ToLower(r.Title), q):
			byTitle = append(byTitle, hit)
		case strings.Contains(strings.ToLower(r.Text), q):
			byText = append(byText, hit)
		}
	}
	return append(byTitle, byText...)
}
