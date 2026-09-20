package directory

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	gplugin "goblog/plugin"

	"github.com/gin-gonic/gin"
)

// ErrBadRepo is the message shown when the submitted text is not a GitHub
// repository.
var ErrBadRepo = errors.New("enter a GitHub repository URL like https://github.com/owner/repo, or just owner/repo")

// repoURLPattern accepts a github.com URL (with or without scheme, www,
// .git or a trailing path) and captures owner and repository. Hostnames are
// case-insensitive, so https://GitHub.com/... is as good as the lower-case
// form.
var repoURLPattern = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(?:/.*)?$`)

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

// submitView is what templates/submit.html renders.
type submitView struct {
	Base   string
	Repo   string // prefilled input
	Error  string // shown above the form
	Done   bool   // queued: show the confirmation instead of the form
	Listed string // "already listed": the plugin's page path
}

// renderSubmit serves the submission form (GET) and handles it (POST).
// The form is plain HTML so it works without JavaScript; the result is
// always rendered into the page, with 429 as the only non-200 status.
func (p *Plugin) renderSubmit(ctx *gplugin.HookContext, base string) (string, gin.H) {
	c := ctx.GinContext
	page := func(v submitView) (string, gin.H) {
		html, err := renderSubmitPage(v)
		if err != nil {
			log.Printf("Directory plugin: render submit: %v", err)
			return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": unavailableHTML, "title": "Submit a plugin"}
		}
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": html, "title": "Submit a plugin"}
	}
	if c.Request.Method != http.MethodPost {
		return page(submitView{Base: base})
	}

	repo := strings.TrimSpace(c.PostForm("repo"))
	// "website" is a honeypot: humans never see it, bots fill it. Pretend
	// it worked so they move on.
	if c.PostForm("website") != "" {
		return page(submitView{Base: base, Done: true})
	}
	_, err := p.svc.Submit(c.Request.Context(), KindPlugin, repo, c.ClientIP(), ctx.Settings["github_token"])
	switch {
	case err == nil:
		return page(submitView{Base: base, Done: true})
	case errors.Is(err, ErrAlreadyListed):
		if key, perr := ParseRepo(repo); perr == nil {
			if name, ok := p.svc.nameOf(key); ok {
				return page(submitView{Base: base, Repo: repo, Error: err.Error(), Listed: base + "/" + name})
			}
		}
		return page(submitView{Base: base, Repo: repo, Error: err.Error()})
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrBusy):
		// gin buffers a status set with c.Status and blog's later
		// Render(c, 200, …) would replace it; writing the header now makes
		// the 429 stick (gin logs a one-line warning when blog then tries
		// 200 — accepted). Content-Type has to be set before the header is
		// flushed too, or net/http sniffs one from the body instead of
		// honouring blog's later Render call.
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Status(http.StatusTooManyRequests)
		c.Writer.WriteHeaderNow()
		return page(submitView{Base: base, Repo: repo, Error: err.Error()})
	default:
		// ErrBadRepo, ErrUnderReview, ErrNameTaken, *ValidationError: all
		// carry a message meant for the submitter. Anything else is an
		// operator problem and is logged, not shown.
		var ve *ValidationError
		if errors.As(err, &ve) || errors.Is(err, ErrBadRepo) || errors.Is(err, ErrUnderReview) || errors.Is(err, ErrNameTaken) {
			return page(submitView{Base: base, Repo: repo, Error: err.Error()})
		}
		log.Printf("Directory plugin: submit %q: %v", repo, err)
		return page(submitView{Base: base, Repo: repo, Error: "Something went wrong on our side; please try again later."})
	}
}
