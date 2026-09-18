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
		rfc1918Class16.MatchString(h), strings.HasPrefix(h, "fd"), strings.HasPrefix(h, "fe80:"):
		return "local network"
	}
	return ""
}

// rfc1918Class16 matches 172.16.0.0/12.
var rfc1918Class16 = regexp.MustCompile(`^172\.(1[6-9]|2[0-9]|3[01])\.`)

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

func renderListing(base string, entries []Entry) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "listing.html", map[string]any{"Base": base, "Entries": entries})
	return buf.String(), err
}

// renderDetail renders a plugin page. d may be nil when the detail JSON could
// not be fetched; notice is shown to the reader in that case.
func renderDetail(base string, e Entry, d *Detail, notice string) (string, error) {
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, "detail.html", map[string]any{
		"Base": base, "Entry": e, "Detail": d, "Notice": notice,
	})
	return buf.String(), err
}
