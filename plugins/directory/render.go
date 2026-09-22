package directory

import (
	"bytes"
	"embed"
	"html/template"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates are html/template, so every index string is escaped and unsafe
// URL schemes are neutralised; only the *_html fields (template.HTML) pass
// through unchanged.
var templates = template.Must(template.New("").Funcs(template.FuncMap{
	// date shows the day part of an RFC 3339 timestamp, or the value as-is.
	"date": func(s string) string {
		if len(s) >= 10 {
			return s[:10]
		}
		return s
	},
	// hostNote flags an allowed_hosts entry broader than one named host;
	// the templates render it as a badge next to the entry.
	"hostNote": hostNote,
}).ParseFS(templateFS, "templates/*.html"))

// hostNote returns a short warning for an allowed_hosts entry that is
// broader than one named host — "*" (anything), another glob pattern, or an
// address on this machine or its local network, which a sandboxed plugin
// should rarely need — and "" for an ordinary host name. The admin plugins
// page has the same rule in JavaScript.
func hostNote(host string) string {
	h := strings.ToLower(host)
	switch {
	case h == "*":
		return "any host"
	case strings.Contains(h, "*"):
		return "wildcard"
	case h == "localhost", h == "::1", h == "0.0.0.0",
		strings.HasPrefix(h, "127."), strings.HasPrefix(h, "10."),
		strings.HasPrefix(h, "192.168."), strings.HasPrefix(h, "169.254."),
		rfc1918Class16.MatchString(h), ipv6Local.MatchString(h):
		return "local network"
	}
	return ""
}

// rfc1918Class16 matches 172.16.0.0/12.
var rfc1918Class16 = regexp.MustCompile(`^172\.(1[6-9]|2[0-9]|3[01])\.`)

// ipv6Local matches unique-local (fc00::/7) and link-local (fe80::/10)
// addresses by their first hextet, so a hostname such as fd.example.test
// is not flagged.
var ipv6Local = regexp.MustCompile(`^(f[cd][0-9a-f]{2}|fe[89ab][0-9a-f]):`)

// namePattern is the registry's rule for plugin names; anything else in a
// sub-path is not a plugin page.
var namePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func validName(name string) bool { return ValidName(name) }

// ValidName reports whether name follows the registry's plugin name rule
// (lowercase letters, digits and hyphens). Callers that pass a plugin name
// to the filesystem (the installer, when it derives a file path) must check
// this before doing so.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// basePath is the page's URL prefix ("/plugins"), taken from the request so
// links keep working if the admin renames the page's slug.
func basePath(c *gin.Context) string {
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	slug, _, _ := strings.Cut(path, "/")
	return "/" + slug
}

// renderListingFor renders the listing page for kind: listing.html for
// plugins, themes-listing.html for themes. query is the visitor's ?q=
// filter ("" for the whole directory) and entries what matched it.
func renderListingFor(kind, base, query string, entries []Entry) (string, error) {
	name := "listing.html"
	if kind == KindTheme {
		name = "themes-listing.html"
	}
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, name, map[string]any{"Base": base, "Query": query, "Entries": entries})
	return buf.String(), err
}

// renderDetailFor renders one entry's page for kind: detail.html for
// plugins, themes-detail.html for themes.
func renderDetailFor(kind, base string, d Detail) (string, error) {
	name := "detail.html"
	if kind == KindTheme {
		name = "themes-detail.html"
	}
	// The page heading is the entry's name; a README's own headings sit
	// under it.
	d.ReadmeHTML = demoteHeadings(d.ReadmeHTML)
	d.ChangelogHTML = demoteHeadings(d.ChangelogHTML)
	for i := range d.Releases {
		d.Releases[i].NotesHTML = demoteHeadings(d.Releases[i].NotesHTML)
	}
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, name, map[string]any{"Base": base, "Entry": d.IndexEntry, "Detail": &d, "Notice": ""})
	return buf.String(), err
}

// reHeadingTag matches an opening or closing h1–h5 tag in the sanitized
// HTML GitHub's markdown API returns.
var reHeadingTag = regexp.MustCompile(`<(/?)h([1-5])([\s>])`)

// demoteHeadings moves every heading in html down one level (h1 → h2, …,
// h5 → h6; h6 stays), so a README rendered under the page's own H1 keeps
// a sensible outline.
func demoteHeadings(html template.HTML) template.HTML {
	return template.HTML(reHeadingTag.ReplaceAllStringFunc(string(html), func(tag string) string {
		m := reHeadingTag.FindStringSubmatch(tag)
		return "<" + m[1] + "h" + string(m[2][0]+1) + m[3]
	}))
}

func renderSubmitPage(v submitView) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "submit.html", v)
	return buf.String(), err
}
