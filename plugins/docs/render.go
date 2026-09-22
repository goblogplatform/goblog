// Package docs serves goblog's builder documentation — how to write and
// publish plugins and themes — from markdown embedded in the binary, so a
// site always documents the version it runs.
package docs

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Heading is one H2/H3 of a rendered page, for its table of contents.
type Heading struct {
	Level int
	ID    string
	Text  string
}

// md is configured once: GitHub-flavoured markdown (tables, strikethrough,
// autolinks), stable heading IDs so sections are linkable, and raw HTML
// allowed — the content is ours and embedded, never user-supplied.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// Render converts one page to HTML and lists its H2/H3 headings in order.
func Render(src []byte) (template.HTML, []Heading, error) {
	doc := md.Parser().Parse(text.NewReader(src))
	// The page's H1 is its title, which the theme renders as the page
	// heading; keep the article to one H1 by leaving it out here.
	if h, ok := doc.FirstChild().(*ast.Heading); ok && h.Level == 1 {
		doc.RemoveChild(doc, h)
	}
	var heads []Heading
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering || h.Level < 2 || h.Level > 3 {
			return ast.WalkContinue, nil
		}
		heads = append(heads, Heading{Level: h.Level, ID: headingID(h), Text: plainText(h, src)})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", nil, err
	}
	return template.HTML(buf.String()), heads, nil
}

// plainText flattens a heading's inline children (text, code spans,
// emphasis) to the words a table of contents shows.
func plainText(n ast.Node, src []byte) string {
	var buf bytes.Buffer
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch t := c.(type) {
			case *ast.Text:
				buf.Write(t.Segment.Value(src))
			case *ast.CodeSpan:
				walk(t)
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return buf.String()
}

// headingID reads the auto-generated id attribute. goldmark stores it as
// []byte today; a string is accepted too so a library change degrades to
// an empty id (and a failing anchor test) rather than a panic at startup.
func headingID(h *ast.Heading) string {
	id, ok := h.AttributeString("id")
	if !ok {
		return ""
	}
	switch v := id.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	}
	return ""
}
