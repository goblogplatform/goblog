package blog

import (
	"bytes"
	"html/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// authorMarkdown renders content written in the admin — posts and pages.
// Raw HTML is kept: the authors are admins and their embeds (YouTube
// iframes, Instagram scripts) depend on it. This is a deliberate decision,
// recorded in docs/superpowers/specs/2026-09-22-server-side-markdown-design.md;
// a post or page can therefore run JavaScript on the site.
var authorMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// commentMarkdown renders what visitors write. Raw HTML is dropped by
// goldmark and whatever markdown produces is then filtered by
// commentPolicy, because commenters are anonymous.
var commentMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// commentPolicy keeps ordinary prose — emphasis, lists, code, links — and
// drops everything else, including javascript: URLs and event handlers.
var commentPolicy = bluemonday.UGCPolicy()

// renderMarkdown turns an author's markdown into HTML, raw HTML included.
// A render error falls back to the escaped source, so a broken document
// shows its text rather than an empty page.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := authorMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

// renderComment turns a visitor's markdown into sanitised HTML.
func renderComment(src string) template.HTML {
	var buf bytes.Buffer
	if err := commentMarkdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(commentPolicy.SanitizeBytes(buf.Bytes()))
}
