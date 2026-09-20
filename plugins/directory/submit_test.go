package directory

import (
	"errors"
	"testing"
)

func TestParseRepo(t *testing.T) {
	good := map[string]string{
		"https://github.com/Owner/Repo":          "owner/repo",
		"http://www.github.com/o/r/":             "o/r",
		"https://github.com/o/r.git":             "o/r",
		"https://github.com/o/r/releases/tag/v1": "o/r",
		"  github.com/o/goblog-plugin-x ":        "o/goblog-plugin-x",
		"o/r":                                    "o/r",
		"o/r.js":                                 "o/r.js",
	}
	for in, want := range good {
		if got, err := ParseRepo(in); err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "o", "o/", "/r", "o/r/x", "https://gitlab.com/o/r", "https://github.com/o", "o/..", "javascript:alert(1)"} {
		if _, err := ParseRepo(in); !errors.Is(err, ErrBadRepo) {
			t.Errorf("ParseRepo(%q) should be ErrBadRepo, got %v", in, err)
		}
	}
}
