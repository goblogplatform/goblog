package blog_test

import (
	"strings"
	"testing"

	. "goblog/blog"
)

// TestPostHTML_KeepsRawHTML: posts are written by admins and their embeds —
// YouTube iframes, Instagram scripts — must survive to the page. This is a
// deliberate decision recorded in the design doc; do not add a sanitiser here.
func TestPostHTML_KeepsRawHTML(t *testing.T) {
	p := Post{Content: "Intro\n\n<iframe src=\"https://www.youtube.com/embed/x\"></iframe>\n\n<script async src=\"//www.instagram.com/embed.js\"></script>\n"}
	got := string(p.HTML())
	for _, want := range []string{`<iframe src="https://www.youtube.com/embed/x">`, `<script async src="//www.instagram.com/embed.js">`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestPostHTML_RendersGFM covers the markdown features the old showdown
// configuration had ({tables: true}) plus the rest of GitHub-flavoured
// markdown, so a migrated theme renders at least what the browser did.
func TestPostHTML_RendersGFM(t *testing.T) {
	p := Post{Content: "# Title\n\nSome **bold** and a [link](/x).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n\n- [x] done\n- [ ] todo\n\n~~struck~~\n"}
	got := string(p.HTML())
	for _, want := range []string{"<h1", "<strong>bold</strong>", `<a href="/x">link</a>`, "<table>", "<td>1</td>", "<code", "func main()", `type="checkbox"`, "<del>struck</del>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// TestCommentHTML_Sanitises: commenters are anonymous, so a comment may use
// ordinary markdown but never raw HTML, scripts, embeds or javascript: URLs.
func TestCommentHTML_Sanitises(t *testing.T) {
	c := Comment{Content: "Nice **post**! See [my site](https://example.test).\n\n<script>alert(1)</script>\n\n<iframe src=\"https://evil.test\"></iframe>\n\n<img src=x onerror=alert(1)>\n\n[click](javascript:alert(1))\n"}
	got := string(c.HTML())
	for _, want := range []string{"<strong>post</strong>", `href="https://example.test"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	// What must go is the executable construct, not any particular payload:
	// a commenter may legitimately write the words alert(1), and a sanitiser
	// that drops a <script> element keeps its text as prose.
	for _, gone := range []string{"<script", "<iframe", "onerror", "javascript:"} {
		if strings.Contains(got, gone) {
			t.Errorf("must not contain %q in %q", gone, got)
		}
	}
}

// TestCommentHTML_InlineHTMLIsInert: raw HTML written inline, rather than as
// its own block, also loses its tags — the words inside remain as prose,
// which is what a reader should see.
func TestCommentHTML_InlineHTMLIsInert(t *testing.T) {
	c := Comment{Content: "Nice post! <script>alert(1)</script> <b onclick=\"steal()\">bold</b> <a href=\"javascript:evil()\">link</a>"}
	got := string(c.HTML())
	for _, gone := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(got, gone) {
			t.Errorf("must not contain %q in %q", gone, got)
		}
	}
	if !strings.Contains(got, "Nice post!") {
		t.Errorf("the comment's own words must survive: %q", got)
	}
}

// TestCommentHTML_PayloadTextIsAllowed: a comment that merely talks about
// JavaScript is prose, not an attack, and must render as written.
func TestCommentHTML_PayloadTextIsAllowed(t *testing.T) {
	c := Comment{Content: "Your snippet calls `alert(1)` — is that on purpose? I would use onerror= instead."}
	got := string(c.HTML())
	for _, want := range []string{"alert(1)", "onerror="} {
		if !strings.Contains(got, want) {
			t.Errorf("prose about code must survive; missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "<script") {
		t.Errorf("nothing executable here: %q", got)
	}
}

// TestPageHTML: a page body is authored in the admin like a post, so it
// renders with the same policy.
func TestPageHTML(t *testing.T) {
	p := Page{Content: "## Heading\n\n<iframe src=\"https://maps.example/embed\"></iframe>\n"}
	got := string(p.HTML())
	if !strings.Contains(got, "<h2") || !strings.Contains(got, "<iframe") {
		t.Errorf("page HTML = %q", got)
	}
}

// TestHTML_EmptyContent: nothing to render is an empty body, not an error
// page or a panic.
func TestHTML_EmptyContent(t *testing.T) {
	if got := string((Post{}).HTML()); strings.TrimSpace(got) != "" {
		t.Errorf("empty post = %q", got)
	}
	if got := string((Comment{}).HTML()); strings.TrimSpace(got) != "" {
		t.Errorf("empty comment = %q", got)
	}
}
