package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The CDN libraries are loaded with defer (#624), so none of them is defined
// while the page is still being parsed. Anything an inline script wants to do
// with one has to wait for DOMContentLoaded, which fires only after every
// deferred script has run.
//
// The tests below guard that arrangement, because it breaks silently: the page
// still renders, it just stops highlighting code, or the login button leads
// nowhere, or a save stores the wrong text. A reviewer is unlikely to catch any
// of that by reading a diff.

// templateFiles are the template sets goblog itself ships. Themes carry their
// own copies of header.html, footer.html, post.html and login.html and are
// checked in their own repositories.
func templateFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, glob := range []string{
		"templates/shared/*.html",
		"themes/default/templates/*.html",
		"plugins/*/templates/*.html",
	} {
		matched, err := filepath.Glob(glob)
		if err != nil {
			t.Fatalf("glob %s: %v", glob, err)
		}
		files = append(files, matched...)
	}
	if len(files) == 0 {
		t.Fatal("no templates found; has the layout moved?")
	}
	return files
}

var scriptSrcTag = regexp.MustCompile(`(?s)<script\b[^>]*\bsrc=[^>]*>`)

// TestExternalScriptsAreDeferred: a <script src> without defer blocks the
// parser until it has been fetched and run.
func TestExternalScriptsAreDeferred(t *testing.T) {
	for _, path := range templateFiles(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, tag := range scriptSrcTag.FindAllString(string(body), -1) {
			if !strings.Contains(tag, " defer") {
				t.Errorf("%s: <script src> is missing defer:\n\t%s", path, tag)
			}
		}
	}
}

var (
	inlineScript = regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`)
	// A statement that opens a DOMContentLoaded handler. Either target works and
	// either quote style does, so the lint does not also police style.
	readyGuard = regexp.MustCompile(`^(?:document|window)\.addEventListener\(\s*["']DOMContentLoaded["']`)
	// Globals that only exist once the deferred scripts have run. admin-script.js
	// is deferred too, so setupEditorTabs belongs here as much as jQuery does.
	deferredGlobals = []string{
		"$(", "$.", "jQuery", "hljs", "SimpleMDE", "inlineAttachment",
		"DOMPurify", "moment(", "bootstrap.", "setupEditorTabs",
	}
)

// TestInlineScriptsWaitForDeferredLibraries: an inline script runs during
// parsing, before any deferred script. It may *declare* a function that uses a
// library — onclick handlers call those long after load, and they have to stay
// global — but it must not *call* into one at the top level.
func TestInlineScriptsWaitForDeferredLibraries(t *testing.T) {
	for _, path := range templateFiles(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range inlineScript.FindAllStringSubmatch(string(body), -1) {
			attrs, script := m[1], m[2]
			if strings.Contains(attrs, "src=") || strings.Contains(attrs, "ld+json") {
				continue
			}
			for _, stmt := range topLevelStatements(script) {
				if readyGuard.MatchString(stmt) {
					continue
				}
				for _, global := range deferredGlobals {
					if strings.Contains(stmt, global) {
						t.Errorf("%s: inline script uses %s while the page is still "+
							"parsing, before the deferred libraries have run. Move it "+
							"into a DOMContentLoaded handler:\n\t%s", path, global, stmt)
						break
					}
				}
			}
		}
	}
}

// topLevelStatements returns the lines of an inline script that sit outside
// every brace and bracket — the code that runs as the parser reaches it, as
// opposed to a function body that runs later. Strings and comments are skipped
// so their punctuation does not throw the depth off. Go template actions are
// dropped for the same reason; a regex literal containing an unbalanced brace
// would still confuse it, but none of the templates has one.
func topLevelStatements(script string) []string {
	script = regexp.MustCompile(`(?s)\{\{.*?\}\}`).ReplaceAllString(script, "0")

	var out []string
	depth := 0
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			out = append(out, trimmed)
		}
		depth += lineDepth(line)
	}
	return out
}

// lineDepth is the net change in nesting a line makes, ignoring anything
// inside a string literal or a comment.
func lineDepth(line string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return depth
		case c == '{' || c == '(' || c == '[':
			depth++
		case c == '}' || c == ')' || c == ']':
			depth--
		}
	}
	return depth
}

// TestEditorTemplatesKeepSimplemdeGlobal: admin-script.js reads `simplemde` as
// a global — updatePost and createPost send simplemde.value() on save, and
// setupEditorTabs reads it to render the Preview tab. Moving the editor's
// construction into a DOMContentLoaded handler (#624) makes it easy to leave
// `var simplemde = new SimpleMDE(...)` inside the handler, which scopes it to
// the handler. Nothing throws: saving just starts sending the hidden
// textarea's stale text instead of what was typed.
func TestEditorTemplatesKeepSimplemdeGlobal(t *testing.T) {
	for _, path := range templateFiles(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		script := string(body)
		if !strings.Contains(script, "new SimpleMDE") {
			continue
		}
		var declaredAtTopLevel bool
		for _, m := range inlineScript.FindAllStringSubmatch(script, -1) {
			if strings.Contains(m[1], "src=") {
				continue
			}
			for _, stmt := range topLevelStatements(m[2]) {
				if stmt == "var simplemde;" || strings.HasPrefix(stmt, "var simplemde =") {
					declaredAtTopLevel = true
				}
			}
		}
		if !declaredAtTopLevel {
			t.Errorf("%s builds a SimpleMDE editor but does not declare `simplemde` at "+
				"the top level of an inline script. admin-script.js reads it as a global; "+
				"scoped to a handler, saving silently sends stale content.", path)
		}
	}
}

// TestReadyGuardAcceptsCommonForms pins which statements count as "this waits
// for DOMContentLoaded", so the lint does not quietly become a style rule. Both
// targets and both quote styles are equivalent; jQuery's own ready helpers are
// not, because they need jQuery to already be defined.
func TestReadyGuardAcceptsCommonForms(t *testing.T) {
	accepted := []string{
		`document.addEventListener("DOMContentLoaded", function () {`,
		`document.addEventListener('DOMContentLoaded', function () {`,
		`window.addEventListener("DOMContentLoaded", () => {`,
		`document.addEventListener( "DOMContentLoaded", init);`,
	}
	rejected := []string{
		`$(function () {`,
		`$(document).ready(function () {`,
		`jQuery(document).ready(function () {`,
		`hljs.highlightAll();`,
		`el.addEventListener("DOMContentLoaded", fn);`, // not document/window
		`init(); document.addEventListener("DOMContentLoaded", fn);`,
	}
	for _, stmt := range accepted {
		if !readyGuard.MatchString(stmt) {
			t.Errorf("should be accepted as a DOMContentLoaded guard: %s", stmt)
		}
	}
	for _, stmt := range rejected {
		if readyGuard.MatchString(stmt) {
			t.Errorf("should not count as a DOMContentLoaded guard: %s", stmt)
		}
	}
}
