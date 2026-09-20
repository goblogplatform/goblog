package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotFound is returned by Source.File when the ref or path does not exist.
var ErrNotFound = errors.New("not found")

// MaxAssetBytes is the largest release asset the registry will download and
// validate (16 MiB); goblog's installer applies the same cap.
const MaxAssetBytes = 16 << 20

// maxAPIBytes caps a JSON or markdown response from the API.
const maxAPIBytes = 8 << 20

// Asset is a file attached to a GitHub release.
type Asset struct {
	ID          int64
	Name        string
	Size        int
	DownloadURL string // browser_download_url
}

// Release is one GitHub release of a plugin repository.
type Release struct {
	Tag         string
	Name        string
	Body        string // release notes, markdown
	URL         string
	PublishedAt time.Time
	Draft       bool
	Prerelease  bool
	Assets      []Asset
}

// Source is what the registry needs from GitHub. It is an interface so the
// validator and builder are tested against an httptest fake.
type Source interface {
	// Releases lists all releases, newest first as GitHub returns them,
	// including drafts and pre-releases (callers filter).
	Releases(ctx context.Context, owner, repo string) ([]Release, error)
	// File returns the contents of path at ref; ErrNotFound when absent.
	File(ctx context.Context, owner, repo, ref, path string) ([]byte, error)
	// ReleaseAsset downloads a release asset by id (at most MaxAssetBytes).
	ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error)
	// RenderMarkdown renders GitHub-flavoured markdown to sanitized HTML in
	// the context of ownerRepo (so `#123` and `@user` references resolve;
	// relative links and images are left as-is).
	RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error)
	// RepoStars returns the repository's GitHub stargazer count (the
	// directory's "top plugins" ordering).
	RepoStars(ctx context.Context, owner, repo string) (stars int, err error)
}

// GitHubSource implements Source with the GitHub REST API over net/http.
// It is deliberately dependency-free: five endpoints do not justify a
// client library in goblog's module graph.
type GitHubSource struct {
	base        string
	token       string
	userAgent   string
	client      *http.Client // API calls
	assetClient *http.Client // asset downloads: the timeout has to cover up to MaxAssetBytes
}

// NewGitHubSource returns a Source for api.github.com (baseURL "") or a
// test server. token may be empty for unauthenticated access (60 requests
// per hour instead of 5000).
func NewGitHubSource(token, baseURL string) *GitHubSource {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &GitHubSource{
		base:        strings.TrimSuffix(baseURL, "/"),
		token:       token,
		userAgent:   "goblog-directory",
		client:      &http.Client{Timeout: 30 * time.Second},
		assetClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// SetUserAgent sets the User-Agent sent to GitHub (which requires one).
func (g *GitHubSource) SetUserAgent(ua string) { g.userAgent = ua }

func (g *GitHubSource) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	return req, nil
}

// do runs req and returns the body of a 2xx response, reading at most limit
// bytes. A 404 is ErrNotFound (wrapped by callers with what was looked up);
// an exhausted rate limit is named explicitly so the operator knows the fix.
func (g *GitHubSource) do(client *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return nil, fmt.Errorf("%s: GitHub API rate limit exceeded (set the directory's github_token setting to raise it)", req.URL.Path)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("%s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", req.URL.Path, limit)
	}
	return b, nil
}

func (g *GitHubSource) getJSON(ctx context.Context, path string, v any) error {
	req, err := g.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	b, err := g.do(g.client, req, maxAPIBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type apiRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"` // null for drafts
	Assets      []struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		Size               int    `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (g *GitHubSource) Releases(ctx context.Context, owner, repo string) ([]Release, error) {
	var out []Release
	for page := 1; ; page++ {
		var rels []apiRelease
		path := fmt.Sprintf("/repos/%s/%s/releases?per_page=100&page=%d", owner, repo, page)
		if err := g.getJSON(ctx, path, &rels); err != nil {
			return nil, fmt.Errorf("list releases for %s/%s: %w", owner, repo, err)
		}
		for _, r := range rels {
			rel := Release{Tag: r.TagName, Name: r.Name, Body: r.Body, URL: r.HTMLURL, Draft: r.Draft, Prerelease: r.Prerelease}
			if r.PublishedAt != nil {
				rel.PublishedAt = *r.PublishedAt
			}
			for _, a := range r.Assets {
				rel.Assets = append(rel.Assets, Asset{ID: a.ID, Name: a.Name, Size: a.Size, DownloadURL: a.BrowserDownloadURL})
			}
			out = append(out, rel)
		}
		if len(rels) < 100 {
			return out, nil
		}
	}
}

func (g *GitHubSource) File(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	var fc struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref), &fc)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%s/%s@%s:%s: %w", owner, repo, ref, path, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	if fc.Type != "file" {
		return nil, fmt.Errorf("%s/%s@%s:%s is not a file", owner, repo, ref, path)
	}
	if fc.Encoding != "base64" {
		return nil, fmt.Errorf("decode %s/%s@%s:%s: unexpected encoding %q", owner, repo, ref, path, fc.Encoding)
	}
	// GitHub wraps the base64 body at 60 columns.
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(fc.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode %s/%s@%s:%s: %w", owner, repo, ref, path, err)
	}
	return b, nil
}

func (g *GitHubSource) ReleaseAsset(ctx context.Context, owner, repo string, assetID int64) ([]byte, error) {
	req, err := g.newRequest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/releases/assets/%d", owner, repo, assetID), nil)
	if err != nil {
		return nil, err
	}
	// With this Accept the API answers with the bytes (via a redirect to
	// the storage host, which the client follows).
	req.Header.Set("Accept", "application/octet-stream")
	b, err := g.do(g.assetClient, req, MaxAssetBytes)
	if err != nil {
		if strings.Contains(err.Error(), "exceeds") {
			return nil, fmt.Errorf("asset %d of %s/%s exceeds %d bytes", assetID, owner, repo, MaxAssetBytes)
		}
		return nil, fmt.Errorf("download asset %d of %s/%s: %w", assetID, owner, repo, err)
	}
	return b, nil
}

func (g *GitHubSource) RepoStars(ctx context.Context, owner, repo string) (int, error) {
	var r struct {
		Stargazers int `json:"stargazers_count"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo), &r); err != nil {
		return 0, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}
	return r.Stargazers, nil
}

func (g *GitHubSource) RenderMarkdown(ctx context.Context, ownerRepo, markdown string) (string, error) {
	if markdown == "" {
		return "", nil
	}
	body, _ := json.Marshal(map[string]string{"text": markdown, "mode": "gfm", "context": ownerRepo})
	req, err := g.newRequest(ctx, http.MethodPost, "/markdown", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	b, err := g.do(g.client, req, maxAPIBytes)
	if err != nil {
		return "", fmt.Errorf("render markdown for %s: %w", ownerRepo, err)
	}
	return string(b), nil
}
