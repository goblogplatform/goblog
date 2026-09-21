package docs

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"goblog/plugin/wasm"
	"goblog/plugins/directory/registry"
)

// internalLink matches /docs/<slug>[#anchor] links in rendered HTML.
var internalLink = regexp.MustCompile(`href="/docs(?:/([a-z0-9-]+))?(?:#([A-Za-z0-9_-]+))?"`)

// samePageLink matches #anchor links in rendered HTML.
var samePageLink = regexp.MustCompile(`href="#([A-Za-z0-9_-]+)"`)

// TestInternalLinksResolve fails when a page links to a slug that is not
// in the manifest or to an anchor that is not a heading on that page, or
// to a same-page anchor that is not one of its own headings.
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
		for _, m := range samePageLink.FindAllStringSubmatch(string(r.HTML), -1) {
			if !strings.Contains(string(r.HTML), ` id="`+m[1]+`"`) {
				t.Errorf("%s links to #%s but has no such heading", pg.File, m[1])
			}
		}
	}
}

// TestLicenseListsMatchRegistry: the pages that spell out the accepted
// licenses name every one the directory accepts.
func TestLicenseListsMatchRegistry(t *testing.T) {
	p := New()
	for _, slug := range []string{"publishing-a-plugin", "publishing-a-theme", "writing-a-plugin"} {
		r, _ := p.rendered(slug)
		for _, l := range registry.KnownLicenses() {
			if !strings.Contains(string(r.HTML), "<code>"+l+"</code>") {
				t.Errorf("%s does not list the %s license", r.File, l)
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
		"setting_groups",

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

// snakeCase matches a <code>snake_case</code> span, with or without a dotted
// prefix (request.sub_path): the last segment is what is linted.
var snakeCase = regexp.MustCompile(`<code>(?:[a-z][a-z0-9_]*\.)*([a-z][a-z0-9]*(?:_[a-z0-9]+)+)</code>`)

// TestSnakeCaseIdentifiersExist lints every <code>snake_case</code> span in
// the rendered pages against the real identifiers in the code, so a rename
// in code without a matching docs change fails CI instead of shipping stale
// documentation.
func TestSnakeCaseIdentifiersExist(t *testing.T) {
	known := knownIdentifiers()
	p := New()
	for _, pg := range pages {
		r, _ := p.rendered(pg.Slug)
		for _, m := range snakeCase.FindAllStringSubmatch(string(r.HTML), -1) {
			if !known[m[1]] {
				t.Errorf("%s names `%s`, which is not an export, host function, wire field or allowlisted name — renamed in code, or a typo?", pg.File, m[1])
			}
		}
	}
	// And the reference page must mention every export and host function.
	api, _ := p.rendered("plugin-api")
	for _, name := range append(append([]string{}, wasm.Exports...), wasm.HostFunctions...) {
		if !strings.Contains(string(api.HTML), "<code>"+name+"</code>") {
			t.Errorf("plugin-api.md does not document %s", name)
		}
	}
}
