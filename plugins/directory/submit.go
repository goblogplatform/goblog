package directory

import (
	"errors"
	"regexp"
	"strings"
)

// ErrBadRepo is the message shown when the submitted text is not a GitHub
// repository.
var ErrBadRepo = errors.New("enter a GitHub repository URL like https://github.com/owner/repo")

// repoURLPattern accepts a github.com URL (with or without scheme, www,
// .git or a trailing path) and captures owner and repository.
var repoURLPattern = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(?:/.*)?$`)

// repoShortPattern accepts bare owner/repo.
var repoShortPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)

// ParseRepo turns what a person typed into the canonical "owner/repo" key
// (lower-case, so the same repository cannot be submitted twice under
// different capitalisation).
func ParseRepo(input string) (string, error) {
	in := strings.TrimSpace(input)
	m := repoURLPattern.FindStringSubmatch(in)
	if m == nil {
		m = repoShortPattern.FindStringSubmatch(in)
	}
	if m == nil {
		return "", ErrBadRepo
	}
	owner, name := m[1], strings.TrimSuffix(m[2], ".git")
	if name == "" || name == "." || name == ".." || owner == "." || owner == ".." {
		return "", ErrBadRepo
	}
	return strings.ToLower(owner + "/" + name), nil
}
