package directory

import (
	"sort"
	"strings"

	"goblog/blog"
	gplugin "goblog/plugin"

	"gorm.io/gorm"
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
func (p *Plugin) Search(_ *gplugin.HookContext, query string) []gplugin.SearchResult {
	if p.svc == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	var results []gplugin.SearchResult
	for _, def := range p.Pages() {
		kind := kindOf(def.PageType)
		base := "/" + pageSlug(p.db, def.PageType, def.Slug)
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

// pageSlug is the slug of the page with pageType, or def when there is
// no such page.
func pageSlug(db *gorm.DB, pageType, def string) string {
	var page blog.Page
	if db == nil || db.Where("page_type = ?", pageType).First(&page).Error != nil || page.Slug == "" {
		return def
	}
	return page.Slug
}
