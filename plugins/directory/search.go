package directory

import (
	"sort"
	"strings"
	"time"

	gplugin "goblog/plugin"
)

// matches reports whether e is a hit for query: a case-insensitive
// substring match on the display name, name, description or author. An
// empty (or blank) query matches everything.
func matches(e Entry, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, field := range []string{e.DisplayName, e.Name, e.Description, e.Author} {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// listing returns kind's approved entries matching query, most-starred
// first (name breaks ties). It is what the listing pages and the site
// search both show.
func (p *Plugin) listing(kind, query string) []Entry {
	_, entries := p.svc.Index(kind)
	var out []Entry
	for _, e := range entries {
		if matches(e, query) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stars != out[j].Stars {
			return out[i].Stars > out[j].Stars
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Search answers the site search (plugin.Searcher) with matching plugins,
// then matching themes, linking to their pages under whatever slug the
// admin gave each page.
func (p *Plugin) Search(ctx *gplugin.HookContext, query string) []gplugin.SearchResult {
	if p.svc == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	var results []gplugin.SearchResult
	for _, def := range p.Pages() {
		kind := kindOf(def.PageType)
		base := "/" + gplugin.PageSlug(ctx.DB, def.PageType, def.Slug)
		label := "Plugin"
		if kind == KindTheme {
			label = "Theme"
		}
		for _, e := range p.listing(kind, query) {
			results = append(results, gplugin.SearchResult{
				Title:   e.DisplayName,
				URL:     base + "/" + e.Name,
				Summary: e.Description,
				Kind:    label,
			})
		}
	}
	return results
}

// Sitemap lists every approved plugin and theme page (plugin.Sitemapper),
// under whatever slug the admin gave each directory page, with the
// entry's release date as its last change.
func (p *Plugin) Sitemap(ctx *gplugin.HookContext) []gplugin.SitemapURL {
	if p.svc == nil {
		return nil
	}
	var urls []gplugin.SitemapURL
	for _, def := range p.Pages() {
		base := "/" + gplugin.PageSlug(ctx.DB, def.PageType, def.Slug)
		for _, e := range p.listing(kindOf(def.PageType), "") {
			u := gplugin.SitemapURL{Loc: base + "/" + e.Name}
			if t, err := time.Parse(time.RFC3339, e.ReleasedAt); err == nil {
				u.LastMod = t
			}
			urls = append(urls, u)
		}
	}
	return urls
}
