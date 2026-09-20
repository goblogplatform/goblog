package guide

import (
	"bufio"
	"bytes"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"goblog/plugin/wasm"
	"goblog/plugins/directory/registry"
)

func read(t *testing.T, file string) []byte {
	t.Helper()
	b, err := Content.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// isFence reports whether a line opens or closes a fenced code block. A
// fence may be indented (inside a list item) and may use ``` or ~~~.
func isFence(line string) bool {
	line = strings.TrimLeft(line, " \t")
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

// prose returns the page with every fenced code block blanked out, so the
// link and code-span lints see only what goldmark would render as prose.
// Lines are kept (blank) so nothing else shifts.
func prose(src []byte) []byte {
	var out bytes.Buffer
	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if isFence(line) {
			inFence = !inFence
			out.WriteByte('\n')
			continue
		}
		if !inFence {
			out.WriteString(line)
		}
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// headingIDs mirrors goldmark's parser.WithAutoHeadingID() (its
// autoHeadingIDs.Generate): ASCII letters and digits are kept, lowercased;
// spaces, '-' and '_' each become '-'; every other byte — punctuation,
// backticks, multibyte runes — is dropped; an empty result is "heading";
// a repeated ID gets -1, -2, … appended until it is unique. The ID is
// generated from the heading's raw source line, so inline code
// contributes its text without the backticks. Only ATX headings at
// column 0 are recognised (no setext, no headings inside blockquotes or
// lists), which is all these pages use. The plugin's own tests check
// the same anchors with real goldmark, so a divergence here fails on
// that side, never silently.
func headingIDs(src []byte) map[string]bool {
	ids := map[string]bool{}
	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if isFence(line) {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimLeft(line, "#")
		if !strings.HasPrefix(text, " ") {
			continue // "#hashtag" is not a heading
		}
		text = strings.TrimSpace(text)
		// An optional closing sequence of #s is not part of the text.
		if i := strings.LastIndex(text, " #"); i >= 0 && strings.Trim(text[i+1:], "#") == "" {
			text = strings.TrimSpace(text[:i])
		}
		var b strings.Builder
		for i := 0; i < len(text); i++ {
			c := text[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
				b.WriteByte(c)
			case c >= 'A' && c <= 'Z':
				b.WriteByte(c + 'a' - 'A')
			case c == ' ', c == '\t', c == '-', c == '_':
				b.WriteByte('-')
			}
		}
		id := b.String()
		if id == "" {
			id = "heading"
		}
		if ids[id] {
			for n := 1; ; n++ {
				if candidate := id + "-" + strconv.Itoa(n); !ids[candidate] {
					id = candidate
					break
				}
			}
		}
		ids[id] = true
	}
	return ids
}

// TestHeadingIDs pins the goldmark rules the port relies on, with the
// cases the real pages exercise: inline code, punctuation, underscores,
// a fenced "# comment", and a repeated heading.
func TestHeadingIDs(t *testing.T) {
	src := []byte("# Plugin API reference\n\n## `<name>.json`\n\n### template_head and template_footer\n\n```sh\n# not a heading\n```\n\n## Next\n\n## Next\n\n## Limits & lifecycle\n\n#notaheading\n")
	got := headingIDs(src)
	want := []string{"plugin-api-reference", "namejson", "template-head-and-template-footer", "next", "next-1", "limits--lifecycle"}
	for _, id := range want {
		if !got[id] {
			t.Errorf("missing id %q in %v", id, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d ids %v, want %d", len(got), got, len(want))
	}
}

// TestPagesHaveTitleH1: every manifest entry exists in Content and opens
// with an H1 equal to its title, and nothing is embedded that the
// manifest does not list.
func TestPagesHaveTitleH1(t *testing.T) {
	want := []Page{
		{"", "Overview", "overview.md"},
		{"writing-a-plugin", "Writing a plugin", "writing-a-plugin.md"},
		{"plugin-api", "Plugin API reference", "plugin-api.md"},
		{"publishing-a-plugin", "Publishing a plugin", "publishing-a-plugin.md"},
		{"writing-a-theme", "Writing a theme", "writing-a-theme.md"},
		{"publishing-a-theme", "Publishing a theme", "publishing-a-theme.md"},
		{"directory-formats", "Directory formats", "directory-formats.md"},
	}
	if !reflect.DeepEqual(Pages, want) {
		t.Errorf("Pages = %+v, want %+v", Pages, want)
	}
	listed := map[string]bool{}
	for _, pg := range Pages {
		listed[pg.File] = true
		src := read(t, pg.File)
		first := ""
		for _, line := range strings.Split(string(src), "\n") {
			if strings.TrimSpace(line) != "" {
				first = line
				break
			}
		}
		if first != "# "+pg.Title {
			t.Errorf("%s must start with %q, got %q", pg.File, "# "+pg.Title, first)
		}
	}
	entries, err := Content.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !listed[e.Name()] {
			t.Errorf("%s is embedded but not in Pages", e.Name())
		}
	}
}

// internalLink matches [text](/docs/<slug>[#anchor]) links in markdown.
var internalLink = regexp.MustCompile(`\]\((/docs(?:/([a-z0-9-]+))?(?:#([A-Za-z0-9_-]+))?)\)`)

// samePageLink matches [text](#anchor) links in markdown.
var samePageLink = regexp.MustCompile(`\]\(#([A-Za-z0-9_-]+)\)`)

// TestInternalLinksResolve fails when a page links to a slug that is not
// in the manifest or to an anchor that is not a heading on that page, or
// to a same-page anchor that is not one of its own headings. Fenced code
// is not linted (it is never rendered as a link) — and no page links
// from inside a fence anyway, which the count check asserts.
func TestInternalLinksResolve(t *testing.T) {
	bySlug := map[string]Page{}
	ids := map[string]map[string]bool{}
	for _, pg := range Pages {
		bySlug[pg.Slug] = pg
		ids[pg.Slug] = headingIDs(read(t, pg.File))
	}
	for _, pg := range Pages {
		raw := read(t, pg.File)
		src := prose(raw)
		if a, b := len(internalLink.FindAll(raw, -1)), len(internalLink.FindAll(src, -1)); a != b {
			t.Errorf("%s has %d /docs links inside fenced code", pg.File, a-b)
		}
		for _, m := range internalLink.FindAllSubmatch(src, -1) {
			slug, anchor := string(m[2]), string(m[3])
			target, ok := bySlug[slug]
			if !ok {
				t.Errorf("%s links to unknown page /docs/%s", pg.File, slug)
				continue
			}
			if anchor == "" {
				continue
			}
			if !ids[slug][anchor] {
				t.Errorf("%s links to /docs/%s#%s but %s has no such heading", pg.File, slug, anchor, target.File)
			}
		}
		for _, m := range samePageLink.FindAllSubmatch(src, -1) {
			if !ids[pg.Slug][string(m[1])] {
				t.Errorf("%s links to #%s but has no such heading", pg.File, m[1])
			}
		}
	}
}

// TestLicenseListsMatchRegistry: the pages that spell out the accepted
// licenses name every one the directory accepts.
func TestLicenseListsMatchRegistry(t *testing.T) {
	for _, file := range []string{"publishing-a-plugin.md", "publishing-a-theme.md", "writing-a-plugin.md"} {
		src := string(read(t, file))
		for _, l := range registry.KnownLicenses() {
			if !strings.Contains(src, "`"+l+"`") {
				t.Errorf("%s does not list the %s license", file, l)
			}
		}
	}
}

// TestNoAbsoluteSelfLinks keeps a self-hosted copy self-contained.
func TestNoAbsoluteSelfLinks(t *testing.T) {
	for _, pg := range Pages {
		if bytes.Contains(read(t, pg.File), []byte("goblog.live/docs")) {
			t.Errorf("%s links to goblog.live/docs; use /docs/<slug>", pg.File)
		}
	}
}

// jsonTags collects the json field names of a struct type (embedded structs
// included), so the docs' field tables are checked against the real shapes.
func jsonTags(t reflect.Type, into map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			jsonTags(f.Type, into)
			continue
		}
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
			into[tag] = true
		}
	}
}

// knownIdentifiers is every snake_case name the docs may put in a code span:
// wasm exports and host functions, the JSON fields of the wire types, and
// an explicit allowlist for setting keys, env vars and file names. A rename
// in code without a docs change fails TestSnakeCaseIdentifiersExist; a new
// term in the docs that is not in code fails it too, which is the point.
func knownIdentifiers() map[string]bool {
	known := map[string]bool{}
	for _, e := range wasm.Exports {
		known[e] = true
	}
	for _, h := range wasm.HostFunctions {
		known[h] = true
	}
	for _, t := range []reflect.Type{reflect.TypeOf(registry.IndexEntry{}), reflect.TypeOf(registry.DetailDoc{}), reflect.TypeOf(registry.ReleaseDoc{}), reflect.TypeOf(registry.ThemeManifest{}), reflect.TypeOf(registry.Manifest{})} {
		jsonTags(t, known)
	}
	for _, w := range wasm.ContractFieldNames() {
		known[w] = true
	}
	for _, s := range []string{
		// Extism's own built-in host function, distinct from goblog's
		// store_* functions in wasm.HostFunctions (plugin-api.md "http").
		"http_request",

		// Template data keys goblog's own templates render, added by
		// blog/blog.go and admin/*.go render calls.
		"nav_pages", "is_admin", "logged_in", "admin_page", "recent_posts",
		"post_type", "post_types", "comment_error", "comment_token", "comment_user",
		"outbound_links", "external_backlinks", "client_id", "email_login_enabled",
		"plugin_settings",

		// Template data keys added by plugin/registry.go and plugin/wasm/wasm.go
		// for plugin-owned pages (plugin-api.md "render_page").
		"has_plugin_content", "plugin_content", "plugin_head_html", "plugin_footer_html",

		// Reserved page slugs a plugin/theme may not claim (plugin/registry.go).
		"test_db", "wizard_db",

		// json tag on plugin/installer.Status and theme/installer.Status
		// (IndexFetchedAt): when the in-memory index copy was last fetched.
		"index_fetched_at",

		// Setting keys seeded in tools/migrate.go and read via
		// blog.SettingValue / ctx.Settings elsewhere.
		"plugin_directory_url", "theme_directory_url", "refresh_minutes", "github_token",
		"site_url", "custom_header_code", "custom_footer_code",

		// comments_require_login is both a template data key (blog/blog.go
		// post.html render calls) and the setting blog.CommentsRequireLogin
		// reads (blog/blog.go, blog/comment.go).
		"comments_require_login",

		// semantic_scholar_id and cache_hours are setting keys of the worked
		// example plugin itself (github.com/goblogplatform/goblog-plugin-scholar,
		// main.go), not part of goblog's own contract.
		"semantic_scholar_id", "cache_hours",
	} {
		known[s] = true
	}
	return known
}

// snakeCase matches a `snake_case` code span, with or without a dotted
// prefix (request.sub_path): the last segment is what is linted.
var snakeCase = regexp.MustCompile("`(?:[a-z][a-z0-9_]*\\.)*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`")

// TestSnakeCaseIdentifiersExist lints every `snake_case` span in the pages
// (outside fenced code) against the real identifiers in the code, so a
// rename in code without a matching docs change fails CI instead of
// shipping stale documentation.
func TestSnakeCaseIdentifiersExist(t *testing.T) {
	known := knownIdentifiers()
	for _, pg := range Pages {
		for _, m := range snakeCase.FindAllSubmatch(prose(read(t, pg.File)), -1) {
			if !known[string(m[1])] {
				t.Errorf("%s names `%s`, which is not an export, host function, wire field or allowlisted name — renamed in code, or a typo?", pg.File, m[1])
			}
		}
	}
	// And the reference page must mention every export and host function.
	api := string(prose(read(t, "plugin-api.md")))
	for _, name := range append(append([]string{}, wasm.Exports...), wasm.HostFunctions...) {
		if !strings.Contains(api, "`"+name+"`") {
			t.Errorf("plugin-api.md does not document %s", name)
		}
	}
}
