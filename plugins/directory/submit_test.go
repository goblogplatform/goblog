package directory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gplugin "goblog/plugin"

	"github.com/gin-gonic/gin"
)

func TestParseRepo(t *testing.T) {
	good := map[string]string{
		"https://github.com/Owner/Repo":          "owner/repo",
		"HTTPS://GitHub.COM/Owner/Repo":          "owner/repo",
		"http://www.github.com/o/r/":             "o/r",
		"https://github.com/o/r.git":             "o/r",
		"https://github.com/o/r/releases/tag/v1": "o/r",
		"  github.com/o/goblog-plugin-x ":        "o/goblog-plugin-x",
		"o/r":                                    "o/r",
		"o/r.js":                                 "o/r.js",
	}
	for in, want := range good {
		if got, err := ParseRepo(in); err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "o", "o/", "/r", "o/r/x", "https://gitlab.com/o/r", "https://github.com/o", "o/..", "javascript:alert(1)"} {
		if _, err := ParseRepo(in); !errors.Is(err, ErrBadRepo) {
			t.Errorf("ParseRepo(%q) should be ErrBadRepo, got %v", in, err)
		}
	}
}

func TestRenderPage_SubmitForm(t *testing.T) {
	f := newPluginFixture(t)
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/submit", "submit", nil)
	tmpl, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if tmpl != "page_content.html" || data["title"] != "Submit a plugin" {
		t.Errorf("form: %q %v", tmpl, data["title"])
	}
	for _, want := range []string{`<form`, `method="post"`, `action="/plugins/submit"`, `name="repo"`, `name="website"`, "goblog-plugin.json", `href="/docs/publishing-a-plugin"`} {
		if !strings.Contains(html, want) {
			t.Errorf("form missing %q in:\n%s", want, html)
		}
	}
}

func TestRenderPage_SubmitForm_Theme(t *testing.T) {
	f := newPluginFixture(t)
	ctx, _ := newRenderCtx(t, http.MethodGet, "/themes/submit", "submit", nil)
	tmpl, data := f.p.RenderPage(ctx, ThemePageType)
	html := content(t, data)
	if tmpl != "page_content.html" || data["title"] != "Submit a theme" {
		t.Errorf("form: %q %v", tmpl, data["title"])
	}
	for _, want := range []string{`<form`, `method="post"`, `action="/themes/submit"`, `name="repo"`, `name="website"`, "goblog-theme.json", `href="/docs/publishing-a-theme"`} {
		if !strings.Contains(html, want) {
			t.Errorf("form missing %q in:\n%s", want, html)
		}
	}
	if strings.Contains(html, "goblog-plugin.json") {
		t.Errorf("theme form must not mention goblog-plugin.json:\n%s", html)
	}
}

func TestRenderPage_SubmitPost(t *testing.T) {
	f := newPluginFixture(t)
	post := func(repo, honeypot string) (gin.H, string) {
		ctx, _ := newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {repo}, "website": {honeypot}})
		_, data := f.p.RenderPage(ctx, PageType)
		return data, content(t, data)
	}

	// Success: queued, row pending.
	_, html := post("https://github.com/o/hello", "")
	if !strings.Contains(html, "Queued for review") {
		t.Errorf("success page:\n%s", html)
	}
	var r Repo
	if err := f.db.Where("repo = ?", "o/hello").First(&r).Error; err != nil || r.Status != StatusPending || r.SubmitterIP != "203.0.113.5" {
		t.Errorf("row = %+v %v", r, err)
	}

	// Under review, then listed.
	if _, html = post("o/hello", ""); !strings.Contains(html, "already under review") {
		t.Errorf("under review:\n%s", html)
	}
	f.svc.Approve(r.ID)
	if _, html = post("o/hello", ""); !strings.Contains(html, "already listed") || !strings.Contains(html, `href="/plugins/hello"`) {
		t.Errorf("already listed:\n%s", html)
	}

	// Validation failure: the registry's message and the form again, prefilled.
	if _, html = post("o/missing", ""); !strings.Contains(html, "no such repository") || !strings.Contains(html, `value="o/missing"`) {
		t.Errorf("validation failure:\n%s", html)
	}
	// Bad input.
	if _, html = post("gitlab.com/o/r", ""); !strings.Contains(html, "enter a GitHub repository URL") {
		t.Errorf("bad input:\n%s", html)
	}
	// Honeypot: success page, nothing stored.
	if _, html = post("o/zeta", "http://spam"); !strings.Contains(html, "Queued for review") {
		t.Errorf("honeypot:\n%s", html)
	}
	if err := f.db.Where("repo = ?", "o/zeta").First(&Repo{}).Error; err == nil {
		t.Error("honeypot submissions must not be stored")
	}

	// The kind comes from the manifest, not the form: a theme pasted into
	// the plugins form is queued as a theme and the confirmation says so.
	ctx, _ := newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {"o/ocean"}})
	_, data := f.p.RenderPage(ctx, PageType)
	if html := content(t, data); !strings.Contains(html, "Queued for review as a theme") || !strings.Contains(html, `<a href="/themes">themes page</a>`) {
		t.Errorf("theme success page:\n%s", html)
	}
	var themeRow Repo
	if err := f.db.Where("repo = ?", "o/ocean").First(&themeRow).Error; err != nil || themeRow.Kind != KindTheme {
		t.Errorf("theme row = %+v %v", themeRow, err)
	}
}

// TestRenderPage_SubmitAlreadyListedLinksToActualKind: a repository already
// listed under one kind, but submitted through the other kind's form, must
// link at the page of the kind it actually has.
func TestRenderPage_SubmitAlreadyListedLinksToActualKind(t *testing.T) {
	f := newPluginFixture(t)
	if _, err := f.svc.Add(context.Background(), KindTheme, "o/ocean", ""); err != nil {
		t.Fatal(err)
	}
	ctx, _ := newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {"o/ocean"}})
	_, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if !strings.Contains(html, "already listed") || !strings.Contains(html, `href="/themes/ocean"`) {
		t.Errorf("already listed under the other kind:\n%s", html)
	}
	// Same kind: the link follows the request's slug (the admin may have
	// renamed the page), not a hard-coded default.
	ctx, _ = newRenderCtx(t, http.MethodPost, "/skins/submit", "submit", url.Values{"repo": {"o/ocean"}})
	_, data = f.p.RenderPage(ctx, ThemePageType)
	if html := content(t, data); !strings.Contains(html, `href="/skins/ocean"`) {
		t.Errorf("same-kind link should use the request slug:\n%s", html)
	}
}

func TestRenderPage_SubmitRateLimitedIs429(t *testing.T) {
	f := newPluginFixture(t)
	var w *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		var ctx *gplugin.HookContext
		ctx, w = newRenderCtx(t, http.MethodPost, "/plugins/submit", "submit", url.Values{"repo": {"o/missing"}})
		f.p.RenderPage(ctx, PageType)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("6th attempt should set 429, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
}
