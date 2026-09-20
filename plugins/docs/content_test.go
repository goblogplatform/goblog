package docs

import (
	"regexp"
	"strings"
	"testing"
)

// internalLink matches /docs/<slug>[#anchor] links in rendered HTML.
var internalLink = regexp.MustCompile(`href="/docs(?:/([a-z0-9-]+))?(?:#([A-Za-z0-9_-]+))?"`)

// TestInternalLinksResolve fails when a page links to a slug that is not
// in the manifest or to an anchor that is not a heading on that page.
func TestInternalLinksResolve(t *testing.T) {
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		for _, m := range internalLink.FindAllStringSubmatch(string(r.HTML), -1) {
			slug, anchor := m[1], m[2]
			target, ok := p.rendered(slug)
			if !ok {
				t.Errorf("%s links to unknown page /docs/%s", pg.File, slug)
				continue
			}
			if anchor == "" {
				continue
			}
			if !strings.Contains(string(target.HTML), ` id="`+anchor+`"`) {
				t.Errorf("%s links to /docs/%s#%s but %s has no such heading", pg.File, slug, anchor, target.File)
			}
		}
	}
}

// TestNoAbsoluteSelfLinks keeps a self-hosted copy self-contained.
func TestNoAbsoluteSelfLinks(t *testing.T) {
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		if strings.Contains(string(r.HTML), "goblog.live/docs") {
			t.Errorf("%s links to goblog.live/docs; use /docs/<slug>", pg.File)
		}
	}
}
