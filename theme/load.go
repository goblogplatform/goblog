package theme

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// FuncMap is the template.FuncMap every theme is loaded with.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}
}

// base parses the templates every theme starts from: the shared set, then
// the default theme. A theme's own files are parsed on top, so a template
// it does not ship falls back to default's instead of 500ing the page.
func base(funcMap template.FuncMap) (*template.Template, error) {
	tmpl, err := template.New("").Funcs(funcMap).ParseGlob(filepath.Join(SharedDir, "*.html"))
	if err != nil {
		return nil, fmt.Errorf("shared templates: %w", err)
	}
	if tmpl, err = tmpl.ParseGlob(filepath.Join(BuiltinRoot, DefaultName, "templates", "*.html")); err != nil {
		return nil, fmt.Errorf("default theme: %w", err)
	}
	return tmpl, nil
}

// Load builds the template set for name. Anything wrong with the named
// theme (unknown, unparsable) is logged and default is loaded instead;
// only a broken shared/default set is an error, because nothing can render
// without it.
func Load(name string, funcMap template.FuncMap) (*template.Template, string, error) {
	tmpl, err := base(funcMap)
	if err != nil {
		return nil, "", err
	}
	if name == DefaultName {
		return tmpl, DefaultName, nil
	}
	dir, ok := Dir(name)
	if !ok {
		log.Printf("Warning: theme %q is invalid or missing, falling back to default", name)
		return tmpl, DefaultName, nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "templates", "*.html"))
	if len(matches) == 0 {
		// The theme directory exists but ships no templates yet; it renders
		// exactly like default, which is a valid (if empty) override.
		return tmpl, name, nil
	}
	if _, err := tmpl.ParseFiles(matches...); err != nil {
		log.Printf("Warning: failed to load theme %q: %v — falling back to default", name, err)
		tmpl, err = base(funcMap)
		return tmpl, DefaultName, err
	}
	return tmpl, name, nil
}

// ValidateFiles checks a theme's files as the installer and the directory
// see them (paths relative to the theme root, e.g. "templates/home.html"):
// there must be at least one template, and each templates/*.html must parse
// on top of the shared and default sets. Files in subdirectories of
// templates/ are not loaded by Load and are ignored here too.
func ValidateFiles(files map[string][]byte) error {
	tmpl, err := base(FuncMap())
	if err != nil {
		return err
	}
	var names []string
	for p := range files {
		if path.Dir(p) == "templates" && strings.HasSuffix(p, ".html") {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return errors.New("no templates/*.html files: a theme must ship at least one template")
	}
	sort.Strings(names) // deterministic first error
	for _, p := range names {
		if _, err := tmpl.New(path.Base(p)).Parse(string(files[p])); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// StaticHandler serves /theme/* from the active theme's static/ directory,
// falling back to default's so an override-only theme still gets the base
// CSS. The path is cleaned as an absolute path first so ".." cannot climb
// out of static/.
func StaticHandler(active func() string) gin.HandlerFunc {
	return func(c *gin.Context) {
		fp := path.Clean("/" + c.Param("filepath"))
		if dir, ok := Dir(active()); ok {
			if p := filepath.Join(dir, "static", filepath.FromSlash(fp)); isFile(p) {
				c.File(p)
				return
			}
		}
		if p := filepath.Join(BuiltinRoot, DefaultName, "static", filepath.FromSlash(fp)); isFile(p) {
			c.File(p)
			return
		}
		c.Status(http.StatusNotFound)
	}
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
