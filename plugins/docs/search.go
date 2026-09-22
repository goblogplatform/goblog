package docs

import (
	"regexp"
	"strings"
	"unicode/utf8"

	gplugin "goblog/plugin"
)

// snippetLen is roughly how many characters of text a search result shows
// around the first match.
const snippetLen = 160

var (
	reMarkdownLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reMarkdownNoise = regexp.MustCompile("(?m)^#+ |[`*>]")
	reSpace         = regexp.MustCompile(`\s+`)
)

// searchText reduces markdown source to searchable text: heading markers,
// emphasis, code ticks and link targets removed, whitespace collapsed.
// Underscores stay: they are identifiers (allowed_hosts) far more often
// than emphasis in these pages.
func searchText(src []byte) string {
	s := reMarkdownLink.ReplaceAllString(string(src), "$1") // links first: their text may hold code spans
	s = reMarkdownNoise.ReplaceAllString(s, "")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// snippet is about snippetLen characters of text around the first
// case-insensitive occurrence of q, cut at word boundaries and marked with
// ellipses where text was left out. With no occurrence it is the start of
// the text.
func snippet(text, q string) string {
	// Lower-casing can change byte length (İ is 2 bytes, i̇ is 3), so the
	// index is approximate for such text and clamped; the boundary walk
	// below lands on a real word boundary either way.
	at := strings.Index(strings.ToLower(text), strings.ToLower(q))
	if at < 0 {
		at = 0
	}
	at = min(at, len(text))
	start := max(at-snippetLen/3, 0)
	end := min(start+snippetLen, len(text))
	// Move to word boundaries so the snippet never starts or ends mid-word.
	// Only ASCII whitespace counts: it is never a UTF-8 continuation byte,
	// so the cut cannot split a rune.
	for start > 0 && !asciiSpace(text[start-1]) {
		start--
	}
	for end < len(text) && !asciiSpace(text[end]) {
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

func asciiSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '\r' }

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

// Sitemap lists every docs page but the index (plugin.Sitemapper) under
// the docs page's current slug; blog lists the page itself.
func (p *Plugin) Sitemap(ctx *gplugin.HookContext) []gplugin.SitemapURL {
	def := p.Pages()[0]
	base := "/" + gplugin.PageSlug(ctx.DB, def.PageType, def.Slug)
	var urls []gplugin.SitemapURL
	for _, pg := range pages {
		if pg.Slug != "" {
			urls = append(urls, gplugin.SitemapURL{Loc: base + "/" + pg.Slug})
		}
	}
	return urls
}

// metaDescriptionLen is the most a page's <meta name="description"> shows.
const metaDescriptionLen = 155

// metaDescription is a page's opening words — its text after the title,
// cut at a word boundary to fit a search snippet — for the <head>.
func metaDescription(r renderedPage) string {
	text := strings.TrimSpace(strings.TrimPrefix(r.Text, r.Title))
	if utf8.RuneCountInString(text) <= metaDescriptionLen {
		return text
	}
	// Leave room for the ellipsis, then back up to a word boundary. The
	// pages are ASCII apart from the odd dash, so a byte offset is close
	// enough to a character count to start from.
	end := metaDescriptionLen - 1
	for end > 0 && !asciiSpace(text[end]) {
		end--
	}
	return strings.TrimRight(text[:end], " ,;:") + "…"
}
