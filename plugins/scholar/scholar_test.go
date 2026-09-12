package scholar

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	gplugin "goblog/plugin"

	scholarlib "github.com/compscidr/scholar"
)

func TestSortArticlesByDateDesc(t *testing.T) {
	articles := []*scholarlib.Article{
		{Title: "Old", Year: 2020, Month: 3, Day: 15},
		{Title: "Newest", Year: 2025, Month: 1, Day: 10},
		{Title: "SameYearLater", Year: 2023, Month: 11, Day: 5},
		{Title: "SameYearEarlier", Year: 2023, Month: 2, Day: 20},
		{Title: "SameYearMonth", Year: 2023, Month: 11, Day: 1},
	}

	sortArticlesByDateDesc(articles)

	expected := []string{"Newest", "SameYearLater", "SameYearMonth", "SameYearEarlier", "Old"}
	for i, title := range expected {
		if articles[i].Title != title {
			t.Errorf("position %d: expected %q, got %q", i, title, articles[i].Title)
		}
	}
}

func TestRenderArticlesHTML_Empty(t *testing.T) {
	result := renderArticlesHTML(nil)
	if !strings.Contains(result, "No publications found") {
		t.Errorf("expected 'No publications found' for empty list, got %q", result)
	}
}

func TestRenderArticlesHTML_WithArticles(t *testing.T) {
	articles := []*scholarlib.Article{
		{
			Title:        "Test Paper",
			Authors:      "Alice, Bob",
			ScholarURL:   "https://scholar.google.com/test",
			Year:         2024,
			Journal:      "Test Journal",
			NumCitations: 42,
		},
	}
	result := renderArticlesHTML(articles)

	if !strings.Contains(result, "Test Paper") {
		t.Error("expected title in output")
	}
	if !strings.Contains(result, "Alice, Bob") {
		t.Error("expected authors in output")
	}
	if !strings.Contains(result, "2024") {
		t.Error("expected year in output")
	}
	if !strings.Contains(result, "Test Journal") {
		t.Error("expected journal in output")
	}
	if !strings.Contains(result, "42 citations") {
		t.Error("expected citation count in output")
	}
	if !strings.Contains(result, `href="https://scholar.google.com/test"`) {
		t.Error("expected scholar URL in href")
	}
}

func TestRenderArticlesHTML_XSSEscaping(t *testing.T) {
	articles := []*scholarlib.Article{
		{
			Title:      `<script>alert("xss")</script>`,
			Authors:    `Bob "the hacker"`,
			ScholarURL: "https://scholar.google.com/safe",
		},
	}
	result := renderArticlesHTML(articles)

	if strings.Contains(result, "<script>") {
		t.Error("title should be HTML-escaped")
	}
	if strings.Contains(result, `"the hacker"`) {
		t.Error("authors should be HTML-escaped")
	}
}

func TestSafeHref(t *testing.T) {
	tests := []struct {
		input string
		safe  bool
	}{
		{"https://scholar.google.com/test", true},
		{"http://example.com", true},
		{"javascript:alert(1)", false},
		{"data:text/html,<h1>hi</h1>", false},
		{"ftp://files.example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		result := safeHref(tt.input)
		if tt.safe && result == "" {
			t.Errorf("expected %q to be safe, got empty", tt.input)
		}
		if !tt.safe && result != "" {
			t.Errorf("expected %q to be blocked, got %q", tt.input, result)
		}
	}
}

// blockedClient mimics Google Scholar refusing a datacenter IP: every request
// gets a 403 with the "automated queries" block page. It counts calls.
type blockedClient struct{ calls int }

func (c *blockedClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("<title>Sorry...</title>your computer or network may be sending automated queries")),
	}, nil
}

func newBlockedPlugin(t *testing.T) (*ScholarPlugin, *blockedClient) {
	t.Helper()
	dir := t.TempDir()
	p := New()
	p.sch = scholarlib.New(filepath.Join(dir, "profiles.json"), filepath.Join(dir, "articles.json"))
	p.sch.SetRequestDelay(0)
	client := &blockedClient{}
	p.sch.SetHTTPClient(client)
	p.scholarOnce.Do(func() {}) // mark the library as initialised so ensureScholar keeps ours
	return p, client
}

func renderResearch(p *ScholarPlugin) string {
	settings := map[string]string{"enabled": "true", "scholar_id": "SbUmSEAAAAAJ", "article_limit": "50"}
	_, data := p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	html, _ := data["plugin_content"].(string)
	return html
}

// TestRenderPage_BlockedByScholar covers issue #547: when Google refuses the
// request, visitors get a friendly message rather than the raw library error
// (which names the Google URL and status). Not re-hitting Google after a
// failure is the scholar library's job (its failure cooldown).
func TestRenderPage_BlockedByScholar(t *testing.T) {
	p, client := newBlockedPlugin(t)

	html := renderResearch(p)
	if client.calls != 1 {
		t.Fatalf("expected exactly one request to Scholar, got %d", client.calls)
	}
	if !strings.Contains(html, "temporarily unavailable") {
		t.Errorf("expected a friendly unavailable message, got %q", html)
	}
	if strings.Contains(html, "scholar.google.com") || strings.Contains(html, "403") {
		t.Errorf("raw error details must not be shown to visitors, got %q", html)
	}
}

// recordingClient answers every request with the given body and status and
// records the requests it saw.
type recordingClient struct {
	status   int
	body     string
	requests []*http.Request
}

func (c *recordingClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	return &http.Response{
		StatusCode: c.status,
		Status:     http.StatusText(c.status),
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}, nil
}

// newPluginWithClient wires a plugin so that ensureScholar builds the library
// around the given fake client, exercising the real settings-driven setup.
func newPluginWithClient(t *testing.T, client *recordingClient) *ScholarPlugin {
	t.Helper()
	p := New()
	p.newScholar = func(profileCache, articleCache string) *scholarlib.Scholar {
		sch := scholarlib.New(profileCache, articleCache)
		sch.SetRequestDelay(0)
		sch.SetHTTPClient(client)
		return sch
	}
	return p
}

const s2Page = `{"offset":0,"data":[{"paperId":"abc","url":"https://www.semanticscholar.org/paper/abc","title":"Paper From Semantic Scholar","authors":[{"name":"Jason B. Ernst"}],"year":2023,"publicationDate":"2023-06-01","venue":"IEEE Access","citationCount":7}]}`

func TestRenderPage_SemanticScholarSource(t *testing.T) {
	client := &recordingClient{status: 200, body: s2Page}
	p := newPluginWithClient(t, client)
	dir := t.TempDir()
	settings := map[string]string{
		"enabled": "true", "source": "semantic_scholar", "semantic_scholar_id": "1792904",
		"scholar_id": "SbUmSEAAAAAJ", "article_limit": "50",
		"profile_cache": filepath.Join(dir, "p.json"), "article_cache": filepath.Join(dir, "a.json"),
	}
	_, data := p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	html, _ := data["plugin_content"].(string)

	if len(client.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(client.requests))
	}
	req := client.requests[0]
	if req.URL.Host != "api.semanticscholar.org" || !strings.Contains(req.URL.Path, "/author/1792904/") {
		t.Errorf("expected a Semantic Scholar request for author 1792904, got %s", req.URL)
	}
	if !strings.Contains(html, "Paper From Semantic Scholar") || !strings.Contains(html, "IEEE Access") {
		t.Errorf("expected the S2 paper to be rendered, got %q", html)
	}
}

func TestRenderPage_SemanticScholarAPIKey(t *testing.T) {
	client := &recordingClient{status: 200, body: s2Page}
	p := newPluginWithClient(t, client)
	dir := t.TempDir()
	settings := map[string]string{
		"enabled": "true", "source": "semantic_scholar", "semantic_scholar_id": "1792904", "semantic_scholar_api_key": "k3y",
		"profile_cache": filepath.Join(dir, "p.json"), "article_cache": filepath.Join(dir, "a.json"),
	}
	p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	if got := client.requests[0].Header.Get("x-api-key"); got != "k3y" {
		t.Errorf("expected the API key header, got %q", got)
	}
}

func TestRenderPage_SemanticScholarMissingID(t *testing.T) {
	client := &recordingClient{status: 200, body: s2Page}
	p := newPluginWithClient(t, client)
	settings := map[string]string{"enabled": "true", "source": "semantic_scholar", "scholar_id": "SbUmSEAAAAAJ"}
	_, data := p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	html, _ := data["plugin_content"].(string)
	if !strings.Contains(html, "Semantic Scholar") || !strings.Contains(html, "not configured") {
		t.Errorf("expected a warning naming the Semantic Scholar id setting, got %q", html)
	}
	if len(client.requests) != 0 {
		t.Errorf("no request should be made without an id, got %d", len(client.requests))
	}
}

func TestRenderPage_UnknownSourceIsVisible(t *testing.T) {
	client := &recordingClient{status: 200, body: s2Page}
	p := newPluginWithClient(t, client)
	settings := map[string]string{"enabled": "true", "source": "semantic-scholar", "scholar_id": "SbUmSEAAAAAJ", "semantic_scholar_id": "1792904"}
	_, data := p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	html, _ := data["plugin_content"].(string)
	if !strings.Contains(html, "semantic-scholar") {
		t.Errorf("expected the unknown source value to be reported, got %q", html)
	}
	if len(client.requests) != 0 {
		t.Errorf("an unknown source must not fall back to fetching, got %d requests", len(client.requests))
	}
}

// The default source is still Google Scholar, keyed by scholar_id.
func TestRenderPage_DefaultSourceIsGoogle(t *testing.T) {
	client := &recordingClient{status: 404, body: ""}
	p := newPluginWithClient(t, client)
	dir := t.TempDir()
	settings := map[string]string{"enabled": "true", "scholar_id": "SbUmSEAAAAAJ", "profile_cache": filepath.Join(dir, "p.json"), "article_cache": filepath.Join(dir, "a.json")}
	p.RenderPage(&gplugin.HookContext{Settings: settings}, "research")
	if len(client.requests) != 1 || client.requests[0].URL.Host != "scholar.google.com" || !strings.Contains(client.requests[0].URL.RawQuery, "SbUmSEAAAAAJ") {
		t.Errorf("expected a Google Scholar request for scholar_id, got %v", client.requests)
	}
}
