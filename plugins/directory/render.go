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
}).ParseFS(templateFS, "templates/*.html"))

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
