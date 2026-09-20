package docs

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	src := []byte("# Title\n\nIntro with `code` and a [link](/docs/plugin-api#exports).\n\n## Exports\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n### Identity export\n\n<table><tr><td>raw</td></tr></table>\n\n## Limits\n")
	html, heads, err := Render(src)
	if err != nil {
		t.Fatal(err)
	}
	out := string(html)
	for _, want := range []string{`<h1 id="title">Title</h1>`, `<h2 id="exports">Exports</h2>`, `<h3 id="identity-export">Identity export</h3>`, `<table>`, `<td>raw</td>`, `<code>code</code>`, `href="/docs/plugin-api#exports"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	want := []Heading{{2, "exports", "Exports"}, {3, "identity-export", "Identity export"}, {2, "limits", "Limits"}}
	if len(heads) != len(want) {
		t.Fatalf("headings = %+v", heads)
	}
	for i := range want {
		if heads[i] != want[i] {
			t.Errorf("heading %d = %+v, want %+v", i, heads[i], want[i])
		}
	}
}

func TestRender_HeadingTextWithInlineCode(t *testing.T) {
	_, heads, err := Render([]byte("## The `identity` export\n"))
	if err != nil || len(heads) != 1 || heads[0].Text != "The identity export" || heads[0].ID != "the-identity-export" {
		t.Errorf("heads = %+v, %v", heads, err)
	}
}
