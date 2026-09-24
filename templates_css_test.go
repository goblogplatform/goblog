package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// #630 took the render-blocking CDN stylesheets apart. Each got a different
// answer, and each answer is easy to undo by accident — a reinstated <link>
// looks perfectly ordinary in a diff — so the arrangement is pinned here.

const headTemplate = "templates/shared/_head.html"

func headSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(headTemplate)
	if err != nil {
		t.Fatalf("read %s: %v", headTemplate, err)
	}
	return string(body)
}

// TestTachyonsIsNotLoaded: Tachyons was 73 KB of render-blocking CSS from
// cdnjs (13 KB gzipped) for four rules, which www/css/base.css now carries.
// The classes must keep working for themes goblog does not ship, so base.css
// is served by goblog rather than living in a theme's stylesheet.
func TestTachyonsIsNotLoaded(t *testing.T) {
	head := headSource(t)
	if strings.Contains(head, "tachyons") {
		t.Error("_head.html loads Tachyons again; www/css/base.css replaced it (#630)")
	}
	if !strings.Contains(head, `href="/css/base.css"`) {
		t.Error("_head.html does not link /css/base.css, so the utility classes it provides are missing")
	}

	base, err := os.ReadFile("www/css/base.css")
	if err != nil {
		t.Fatalf("read www/css/base.css: %v", err)
	}
	// The four Tachyons classes goblog's templates and themes actually use.
	for _, sel := range []string{".cover", ".bg-left", ".bg-center-l", ".striped--light-gray"} {
		if !strings.Contains(string(base), sel) {
			t.Errorf("www/css/base.css is missing %s, which templates still use", sel)
		}
	}
}

// TestFontAwesomeDoesNotBlockRendering: icons are decorative, so Font Awesome
// (20 KB gzipped, under 1% of its rules matched) loads against the print media
// type and switches to all on load. The whole sheet is deliberately kept
// rather than subsetted: plugins, custom_header_code and post bodies use icons
// that appear in no template, and a subset would silently drop them.
func TestFontAwesomeDoesNotBlockRendering(t *testing.T) {
	head := headSource(t)
	link := regexp.MustCompile(`<link[^>]*font-awesome[^>]*>`)
	found := link.FindAllString(head, -1)
	if len(found) != 2 {
		t.Fatalf("expected two font-awesome links (the async one and its <noscript> fallback), got %d", len(found))
	}
	var async, fallback int
	for _, tag := range found {
		switch {
		case strings.Contains(tag, `media="print"`) && strings.Contains(tag, `onload="this.media='all'"`):
			async++
		default:
			fallback++
		}
	}
	if async != 1 {
		t.Error(`font-awesome should be loaded with media="print" onload="this.media='all'" so it does not block the first paint (#630)`)
	}
	if fallback != 1 || !strings.Contains(head, "<noscript>") {
		t.Error("font-awesome needs a plain <noscript> fallback for browsers with JS disabled")
	}
}

// TestBootstrapSocialIsLoginOnly: .btn-social appears only in login.html, and
// bootstrap-social matched no rule on any other page, so it is gated behind
// the login_page flag that blog.Login sets.
func TestBootstrapSocialIsLoginOnly(t *testing.T) {
	head := headSource(t)
	idx := strings.Index(head, "bootstrap-social")
	if idx < 0 {
		t.Fatal("_head.html no longer loads bootstrap-social; the login page needs it for .btn-social")
	}
	// The nearest preceding conditional must be the login_page one.
	before := head[:idx]
	if open := strings.LastIndex(before, "{{ if "); open < 0 || !strings.Contains(before[open:], ".login_page") {
		t.Error("bootstrap-social is not gated on .login_page, so every page pays for a sheet only the login page uses (#630)")
	}
}
