# Scholar WASM Plugin (sub-project C) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `goblogplatform/goblog-plugin-scholar` — the Research/publications page as a directory-installable WebAssembly plugin, replacing goblog's compiled-in scholar plugin.

**Architecture:** Standard Go compiled to wasip1. Pure logic (Semantic Scholar JSON → articles, sorting, HTML rendering, cache freshness) lives in files with no PDK imports and is unit-tested natively; the exports and host calls (`pdk` HTTP, `store_get/set`) live in `//go:build wasip1` files. `render_page` serves from the store cache, fetching when missing/stale; a `refresh` job keeps it warm.

**Tech Stack:** Go 1.25 (toolchain 1.26), `github.com/extism/go-pdk` v1.1.3, GitHub Actions release workflow (as in hello v2).

**Spec:** `docs/superpowers/specs/2026-09-18-wasm-plugins-design.md` §4.

## Global Constraints

- Identity `scholar` / `Scholar Publications` / `2.0.0`; page type `research`, slug `research`, title `Research`, `show_in_nav` true, `nav_order` 20 — same as the compiled-in plugin so settings and the page row carry over.
- Settings (keys must match the old ones where they exist): `enabled` (default `false`), `semantic_scholar_id`, `semantic_scholar_api_key`, `article_limit` (default `50`), `cache_hours` (default `24`). Google Scholar is dropped.
- Store key `articles` holds `{"fetched_at": RFC3339, "articles": [...]}`; a render with a fresh cache makes no HTTP call; stale/missing → fetch, store, render; fetch failure with a cache → render the stale cache; with no cache → the notice `<div class="alert alert-warning" role="alert">Publications are temporarily unavailable. Please check back later.</div>`; missing author id → `<div class="alert alert-warning" role="alert">Semantic Scholar Author ID not configured. Set it in the Scholar Publications plugin settings.</div>`.
- Semantic Scholar: `GET https://api.semanticscholar.org/graph/v1/author/{id}/papers?fields=title,authors,year,publicationDate,venue,journal,citationCount,url&limit=100&offset=N`, paginate via `next` until `article_limit` papers; header `x-api-key` when set; `allowed_hosts: ["api.semanticscholar.org"]`.
- HTML matches the compiled-in `renderArticlesHTML`: per article a bordered div with title (linked only through `safeHref` — http/https only), authors line, meta line `year · journal · N citations`; empty → `<p>No publications found.</p>`. Sort: year desc, then publication date desc, then citations desc.
- Jobs: `refresh` every 3600 s; `run_job` re-fetches only when the cache is older than `cache_hours` (so the job interval doesn't need to match the setting), and only when `enabled` and an id is set.
- Repo `goblogplatform/goblog-plugin-scholar` (public, Apache-2.0), checkout `~/dev/goblog-plugin-scholar`; branch `feat/initial`, PR, release `v2.0.0` after merge (the maintainer merges; the controller releases). Commit trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.

---

## File structure

| File | Build | Responsibility |
|---|---|---|
| `go.mod` | | module `github.com/goblogplatform/goblog-plugin-scholar`, go 1.25.0, toolchain go1.26.1, go-pdk v1.1.3 |
| `article.go` | all | `Article`, `parsePapersPage`, `sortArticles`, `renderArticlesHTML`, `safeHref`, cache freshness |
| `article_test.go` | native | unit tests with a recorded API page |
| `fetch.go` | all | `fetchAll(get getter, id, key string, limit int) ([]Article, error)` — pagination over an injected `getter` |
| `main.go` | wasip1 | exports, host imports, PDK HTTP `getter`, store cache |
| `main_native.go` | !wasip1 | `func main() {}` so native `go test` compiles |
| `goblog-plugin.json`, `README.md`, `CHANGELOG.md`, `LICENSE`, `.gitignore`, `.github/workflows/release.yml` | | packaging |

---

### Task 1: Repo, pure logic with native tests

- [ ] **Step 1: Create the repo**

```bash
cd ~/dev && gh repo create goblogplatform/goblog-plugin-scholar --public \
  --description "goblog plugin: a Research page listing your publications from Semantic Scholar (WebAssembly)" --clone
cd ~/dev/goblog-plugin-scholar && git checkout -b feat/initial 2>/dev/null || git checkout -b feat/initial
cp ~/dev/goblog/LICENSE LICENSE; printf 'plugin.wasm\n*.test\n' > .gitignore
cat > go.mod <<'EOM'
module github.com/goblogplatform/goblog-plugin-scholar

go 1.25.0

toolchain go1.26.1

require github.com/extism/go-pdk v1.1.3
EOM
```

- [ ] **Step 2: Failing tests** — `article_test.go`:

```go
package main

import (
	"strings"
	"testing"
	"time"
)

const pageJSON = `{"offset":0,"next":2,"data":[
 {"paperId":"p1","url":"https://www.semanticscholar.org/paper/p1","title":"Old & Cited","year":2019,"publicationDate":"2019-03-01","venue":"Conf A","journal":{"name":"J. A"},"citationCount":40,"authors":[{"name":"A. One"},{"name":"B. Two"}]},
 {"paperId":"p2","url":"javascript:alert(1)","title":"<Newest>","year":2024,"publicationDate":"2024-06-01","venue":"","journal":null,"citationCount":1,"authors":[{"name":"C. Three"}]}
]}`

func TestParsePapersPage(t *testing.T) {
	page, err := parsePapersPage([]byte(pageJSON))
	if err != nil {
		t.Fatal(err)
	}
	if page.Next == nil || *page.Next != 2 || len(page.Articles) != 2 {
		t.Fatalf("page = %+v", page)
	}
	a := page.Articles[0]
	if a.Title != "Old & Cited" || a.Authors != "A. One, B. Two" || a.Year != 2019 || a.Journal != "J. A" || a.Citations != 40 || a.URL != "https://www.semanticscholar.org/paper/p1" || a.Date != "2019-03-01" {
		t.Errorf("article = %+v", a)
	}
	if page.Articles[1].Journal != "" {
		t.Errorf("no journal/venue should be empty, got %q", page.Articles[1].Journal)
	}
	if _, err := parsePapersPage([]byte("{")); err == nil {
		t.Error("bad JSON should error")
	}
}

func TestSortArticles(t *testing.T) {
	as := []Article{
		{Title: "b", Year: 2020, Date: "2020-01-01", Citations: 5},
		{Title: "c", Year: 2021, Date: "2021-01-01", Citations: 1},
		{Title: "a", Year: 2020, Date: "2020-05-01", Citations: 2},
		{Title: "d", Year: 2020, Date: "2020-05-01", Citations: 9},
	}
	sortArticles(as)
	got := as[0].Title + as[1].Title + as[2].Title + as[3].Title
	if got != "cdab" {
		t.Errorf("order = %s, want cdab (year desc, date desc, citations desc)", got)
	}
}

func TestRenderArticlesHTML(t *testing.T) {
	if got := renderArticlesHTML(nil); got != "<p>No publications found.</p>" {
		t.Errorf("empty = %q", got)
	}
	page, _ := parsePapersPage([]byte(pageJSON))
	html := renderArticlesHTML(page.Articles)
	for _, want := range []string{`href="https://www.semanticscholar.org/paper/p1"`, "Old &amp; Cited", "A. One, B. Two", "2019 &middot; J. A &middot; 40 citations", "&lt;Newest&gt;", "2024 &middot; 1 citations"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in\n%s", want, html)
		}
	}
	if strings.Contains(html, "javascript:") {
		t.Error("unsafe URL must not be linked")
	}
	if safeHref("ftp://x") != "" || safeHref("https://ok.test/a?b=1") != "https://ok.test/a?b=1" {
		t.Error("safeHref rules")
	}
}

func TestCacheFreshness(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := cachedArticles{FetchedAt: now.Add(-2 * time.Hour)}
	if !c.fresh(now, 24) || c.fresh(now, 1) {
		t.Error("freshness by cache_hours")
	}
	if (cachedArticles{}).fresh(now, 24) {
		t.Error("zero FetchedAt is never fresh")
	}
	if parseIntSetting("", 50, 0) != 50 || parseIntSetting("7", 50, 0) != 7 || parseIntSetting("x", 50, 0) != 50 || parseIntSetting("0", 50, 0) != 50 {
		t.Error("parseIntSetting")
	}
	if parseIntSetting("9999", 50, maxArticleLimit) != 500 || parseIntSetting("500", 50, maxArticleLimit) != 500 || parseIntSetting("9999", 24, 0) != 9999 {
		t.Error("parseIntSetting clamp")
	}
}
```

`fetch_test.go`:
```go
package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFetchAll(t *testing.T) {
	calls := []string{}
	get := func(url string, headers map[string]string) (int, []byte, error) {
		calls = append(calls, url)
		if headers["x-api-key"] != "k" {
			t.Errorf("api key header missing: %v", headers)
		}
		if strings.Contains(url, "offset=0") {
			return 200, []byte(`{"offset":0,"next":2,"data":[{"title":"a","year":1},{"title":"b","year":2}]}`), nil
		}
		return 200, []byte(`{"offset":2,"next":null,"data":[{"title":"c","year":3}]}`), nil
	}
	as, err := fetchAll(get, "123", "k", 10)
	if err != nil || len(as) != 3 {
		t.Fatalf("got %d articles, %v", len(as), err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "https://api.semanticscholar.org/graph/v1/author/123/papers?") || !strings.Contains(calls[0], "limit=100") || !strings.Contains(calls[1], "offset=2") {
		t.Errorf("calls = %v", calls)
	}
	// limit keeps the newest N: every page is still read, then the sorted
	// result is truncated
	calls = nil
	as, _ = fetchAll(get, "123", "k", 1)
	if len(as) != 1 || len(calls) != 2 || as[0].Title != "c" {
		t.Errorf("limit=1: %d articles, %d calls, first %+v", len(as), len(calls), as)
	}
	// non-200 → error
	bad := func(string, map[string]string) (int, []byte, error) { return 429, []byte("slow down"), nil }
	if _, err := fetchAll(bad, "123", "", 5); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("429 should error with status, got %v", err)
	}
	failing := func(string, map[string]string) (int, []byte, error) { return 0, nil, errors.New("net down") }
	if _, err := fetchAll(failing, "123", "", 5); err == nil {
		t.Error("transport error should propagate")
	}
	if _, err := fetchAll(get, "", "", 5); err == nil {
		t.Error("empty id should error")
	}
}
```

`main_native.go`:
```go
//go:build !wasip1

package main

func main() {}
```

Run: `GOTOOLCHAIN=go1.26.1 go test ./...` → FAIL to compile.

- [ ] **Step 3: Implement** `article.go`:

```go
package main

import (
	"encoding/json"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Article is one publication as rendered on the Research page.
type Article struct {
	Title     string `json:"title"`
	Authors   string `json:"authors"`
	URL       string `json:"url"`
	Year      int    `json:"year"`
	Date      string `json:"date"` // publicationDate, YYYY-MM-DD when known
	Journal   string `json:"journal"`
	Citations int    `json:"citations"`
}

type papersPage struct {
	Next     *int
	Articles []Article
}

// parsePapersPage decodes one page of /author/{id}/papers.
func parsePapersPage(b []byte) (papersPage, error) {
	var raw struct {
		Next *int `json:"next"`
		Data []struct {
			URL             string `json:"url"`
			Title           string `json:"title"`
			Year            int    `json:"year"`
			PublicationDate string `json:"publicationDate"`
			Venue           string `json:"venue"`
			CitationCount   int    `json:"citationCount"`
			Authors         []struct {
				Name string `json:"name"`
			} `json:"authors"`
			Journal *struct {
				Name string `json:"name"`
			} `json:"journal"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return papersPage{}, err
	}
	page := papersPage{Next: raw.Next}
	for _, p := range raw.Data {
		names := make([]string, 0, len(p.Authors))
		for _, a := range p.Authors {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		journal := p.Venue
		if p.Journal != nil && p.Journal.Name != "" {
			journal = p.Journal.Name
		}
		page.Articles = append(page.Articles, Article{
			Title: p.Title, Authors: strings.Join(names, ", "), URL: p.URL, Year: p.Year,
			Date: p.PublicationDate, Journal: journal, Citations: p.CitationCount,
		})
	}
	return page, nil
}

// sortArticles orders newest first: year, then publication date, then citations.
func sortArticles(as []Article) {
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].Year != as[j].Year {
			return as[i].Year > as[j].Year
		}
		if as[i].Date != as[j].Date {
			return as[i].Date > as[j].Date
		}
		return as[i].Citations > as[j].Citations
	})
}

// safeHref allows only absolute http(s) URLs into href attributes.
func safeHref(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return html.EscapeString(u.String())
}

// renderArticlesHTML is the same markup goblog's compiled-in scholar plugin produced.
func renderArticlesHTML(articles []Article) string {
	if len(articles) == 0 {
		return `<p>No publications found.</p>`
	}
	var b strings.Builder
	for _, a := range articles {
		b.WriteString(`<div style="margin-bottom: 12px; padding-bottom: 12px; border-bottom: 1px solid #eee;">`)
		if href := safeHref(a.URL); href != "" {
			b.WriteString(`<div><a href="` + href + `">` + html.EscapeString(a.Title) + `</a></div>`)
		} else {
			b.WriteString(`<div>` + html.EscapeString(a.Title) + `</div>`)
		}
		if a.Authors != "" {
			b.WriteString(`<div style="color: #666; font-size: 13px;">` + html.EscapeString(a.Authors) + `</div>`)
		}
		var meta []string
		if a.Year > 0 {
			meta = append(meta, strconv.Itoa(a.Year))
		}
		if a.Journal != "" {
			meta = append(meta, html.EscapeString(a.Journal))
		}
		if a.Citations > 0 {
			meta = append(meta, strconv.Itoa(a.Citations)+" citations")
		}
		if len(meta) > 0 {
			b.WriteString(`<div style="color: #888; font-size: 13px;">` + strings.Join(meta, " &middot; ") + `</div>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// cachedArticles is what the plugin keeps under the store key "articles".
type cachedArticles struct {
	FetchedAt time.Time `json:"fetched_at"`
	Articles  []Article `json:"articles"`
}

func (c cachedArticles) fresh(now time.Time, cacheHours int) bool {
	if c.FetchedAt.IsZero() {
		return false
	}
	return now.Sub(c.FetchedAt) < time.Duration(cacheHours)*time.Hour
}

// maxArticleLimit caps article_limit: more than this is never useful on a
// single page, and it keeps fetchAll's page bound meaningful.
const maxArticleLimit = 500

// parseIntSetting reads a positive integer setting with a default, clamped
// to max when max > 0.
func parseIntSetting(s string, def, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}

const (
	unavailableHTML = `<div class="alert alert-warning" role="alert">Publications are temporarily unavailable. Please check back later.</div>`
	noIDHTML        = `<div class="alert alert-warning" role="alert">Semantic Scholar Author ID not configured. Set it in the Scholar Publications plugin settings.</div>`
)
```

`fetch.go`:
```go
package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

const (
	apiBase  = "https://api.semanticscholar.org/graph/v1"
	fields   = "title,authors,year,publicationDate,venue,journal,citationCount,url"
	pageSize = 100
)

// getter performs an HTTP GET; the wasm build uses the PDK, tests inject a fake.
type getter func(url string, headers map[string]string) (status int, body []byte, err error)

// maxPages bounds pagination. article_limit is clamped to maxArticleLimit
// (500), so maxPages*pageSize = 1000 is the most the fetch ever
// needs; an author with more papers than that gets the newest 1000 sorted
// and truncated like everyone else.
const maxPages = 10

// fetchAll pages through an author's papers, sorts them newest first, and
// keeps the first limit entries. All pages are read before truncating so
// "limit" means the newest N, not the API's first N.
func fetchAll(get getter, authorID, apiKey string, limit int) ([]Article, error) {
	if authorID == "" {
		return nil, errors.New("semantic scholar author id is empty")
	}
	headers := map[string]string{"Accept": "application/json"}
	if apiKey != "" {
		headers["x-api-key"] = apiKey
	}
	var out []Article
	offset := 0
	for page := 0; page < maxPages; page++ {
		u := fmt.Sprintf("%s/author/%s/papers?fields=%s&limit=%d&offset=%d", apiBase, url.PathEscape(authorID), fields, pageSize, offset)
		status, body, err := get(u, headers)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("semantic scholar returned HTTP %d: %s", status, truncate(string(body), 200))
		}
		p, err := parsePapersPage(body)
		if err != nil {
			return nil, fmt.Errorf("decode semantic scholar response: %w", err)
		}
		out = append(out, p.Articles...)
		if p.Next == nil || len(p.Articles) == 0 {
			break
		}
		offset = *p.Next
	}
	sortArticles(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

In `TestFetchAll`, the `limit=1` case therefore expects `len(as) == 1` **and** `len(calls) == 2` (both pages are read before truncating), and the kept article is the newest (`year 3`, title `c`) — assert `as[0].Title == "c"`.

Run: `GOTOOLCHAIN=go1.26.1 go test ./...` → PASS (native).

- [ ] **Step 4: Commit** — `git add -A && git commit -m "Scholar publications plugin: article model, Semantic Scholar client and rendering

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"`

---

### Task 2: WASM exports, packaging, PR

- [ ] **Step 1: `main.go`** (`//go:build wasip1`):

```go
//go:build wasip1

package main

import (
	"encoding/json"
	"time"

	pdk "github.com/extism/go-pdk"
)

//go:wasmimport extism:host/user store_get
func hostStoreGet(uint64) uint64

//go:wasmimport extism:host/user store_set
func hostStoreSet(uint64, uint64) uint64

func storeGet(key string) ([]byte, bool) {
	k := pdk.AllocateString(key)
	defer k.Free()
	off := hostStoreGet(k.Offset())
	if off == 0 {
		return nil, false
	}
	return pdk.FindMemory(off).ReadBytes(), true
}

func storeSet(key string, value []byte) bool {
	k := pdk.AllocateString(key)
	defer k.Free()
	v := pdk.AllocateBytes(value)
	defer v.Free()
	return hostStoreSet(k.Offset(), v.Offset()) == 0
}

type hookInput struct {
	Settings map[string]string `json:"settings"`
	Request  struct {
		SubPath string `json:"sub_path"`
	} `json:"request"`
}

type jobInput struct {
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings"`
}

type setting struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

func outputJSON(v any) int32 {
	if err := pdk.OutputJSON(v); err != nil {
		pdk.SetErrorString("encode output: " + err.Error())
		return 1
	}
	return 0
}

func pdkGet(url string, headers map[string]string) (int, []byte, error) {
	req := pdk.NewHTTPRequest(pdk.MethodGet, url)
	for k, v := range headers {
		req.SetHeader(k, v)
	}
	resp := req.Send()
	return int(resp.Status()), resp.Body(), nil
}

//go:wasmexport identity
func identity() int32 {
	return outputJSON(map[string]string{"name": "scholar", "display_name": "Scholar Publications", "version": "2.0.0"})
}

//go:wasmexport settings
func settings() int32 {
	return outputJSON([]setting{
		{Key: "enabled", Type: "text", Default: "false", Label: "Enabled", Description: "Set to 'true' to enable the Research page"},
		{Key: "semantic_scholar_id", Type: "text", Default: "", Label: "Semantic Scholar Author ID", Description: "The number at the end of your semanticscholar.org author URL (e.g. 1792904)"},
		{Key: "semantic_scholar_api_key", Type: "text", Default: "", Label: "Semantic Scholar API Key", Description: "Optional; raises the API rate limit"},
		{Key: "article_limit", Type: "text", Default: "50", Label: "Article Limit", Description: "Maximum number of publications to show"},
		{Key: "cache_hours", Type: "text", Default: "24", Label: "Cache Hours", Description: "How long fetched publications are reused before refreshing"},
	})
}

//go:wasmexport pages
func pages() int32 {
	return outputJSON([]map[string]any{{"page_type": "research", "title": "Research", "slug": "research", "show_in_nav": true, "nav_order": 20, "description": "Publications from Semantic Scholar"}})
}

//go:wasmexport jobs
func jobs() int32 {
	return outputJSON([]map[string]any{{"name": "refresh", "interval_seconds": 3600}})
}

// loadCache reads the stored articles; ok is false when absent or undecodable.
func loadCache() (cachedArticles, bool) {
	b, ok := storeGet("articles")
	if !ok {
		return cachedArticles{}, false
	}
	var c cachedArticles
	if err := json.Unmarshal(b, &c); err != nil {
		return cachedArticles{}, false
	}
	return c, true
}

// refresh fetches and stores when the cache is missing or stale. It returns
// the articles to render (fresh, newly fetched, or stale-as-fallback) and
// whether any are available.
func refresh(settings map[string]string, force bool) ([]Article, bool) {
	id := settings["semantic_scholar_id"]
	if id == "" {
		return nil, false
	}
	hours := parseIntSetting(settings["cache_hours"], 24, 0)
	limit := parseIntSetting(settings["article_limit"], 50, maxArticleLimit)
	cache, have := loadCache()
	if have && !force && cache.fresh(time.Now(), hours) {
		return cache.Articles, true
	}
	articles, err := fetchAll(pdkGet, id, settings["semantic_scholar_api_key"], limit)
	if err != nil {
		pdk.Log(pdk.LogWarn, "semantic scholar fetch failed: "+err.Error())
		if have {
			return cache.Articles, true // stale beats nothing
		}
		return nil, false
	}
	b, _ := json.Marshal(cachedArticles{FetchedAt: time.Now(), Articles: articles})
	if !storeSet("articles", b) {
		pdk.Log(pdk.LogWarn, "could not store the publications cache")
	}
	return articles, true
}

//go:wasmexport render_page
func renderPage() int32 {
	var in hookInput
	if err := json.Unmarshal(pdk.Input(), &in); err != nil {
		pdk.SetErrorString("render_page: " + err.Error())
		return 1
	}
	if in.Request.SubPath != "" {
		return outputJSON(map[string]any{}) // no sub-pages: goblog 404s
	}
	if in.Settings["semantic_scholar_id"] == "" {
		return outputJSON(map[string]any{"html": noIDHTML})
	}
	articles, ok := refresh(in.Settings, false)
	if !ok {
		return outputJSON(map[string]any{"html": unavailableHTML})
	}
	return outputJSON(map[string]any{"html": renderArticlesHTML(articles)})
}

//go:wasmexport run_job
func runJob() int32 {
	var in jobInput
	if err := json.Unmarshal(pdk.Input(), &in); err != nil {
		pdk.SetErrorString("run_job: " + err.Error())
		return 1
	}
	// goblog's wasm adapter never calls run_job while the plugin's
	// `enabled` setting is off (plugin/wasm/wasm.go, ScheduledJobs), so
	// only the id needs checking here.
	if in.Name == "refresh" && in.Settings["semantic_scholar_id"] != "" {
		refresh(in.Settings, false) // only fetches when stale
	}
	return outputJSON(map[string]any{})
}

func main() {}
```
(`pdk.AllocateBytes` exists in go-pdk 1.1.3; if not, use `pdk.AllocateString(string(value))`.)

- [ ] **Step 2: Packaging**

`goblog-plugin.json`:
```json
{
  "name": "scholar",
  "display_name": "Scholar Publications",
  "description": "A Research page listing your publications from Semantic Scholar, with citation counts, cached and refreshed daily.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "runtime": "wasm",
  "entry": "plugin.wasm",
  "allowed_hosts": ["api.semanticscholar.org"],
  "min_goblog_version": "0.2.9",
  "homepage": "https://github.com/goblogplatform/goblog-plugin-scholar"
}
```
`.github/workflows/release.yml` — identical to hello v2's (checkout + setup-go with `go-version-file`, build with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .`, `gh release upload "$TAG" plugin.wasm --clobber`, `permissions: contents: write`), plus a `test` job on `push`/`pull_request` running `go test ./...`.
`README.md`: what it does; Install (Admin → Plugins → Scholar Publications → Install; goblog ≥ 0.2.9); Settings table (the five keys; note that installs upgrading from the compiled-in plugin keep `enabled`, `semantic_scholar_id`, `semantic_scholar_api_key`, `article_limit`, and that Google Scholar/`source`/`scholar_id` are gone); "Talks to api.semanticscholar.org only"; caching (store, `cache_hours`, hourly job); build instructions; License.
`CHANGELOG.md`: `## 2.0.0 — first release as a WebAssembly plugin; replaces goblog's compiled-in scholar plugin (Semantic Scholar only).`

- [ ] **Step 3: Build, validate, live check**

```bash
GOTOOLCHAIN=go1.26.1 go test ./... && GOOS=wasip1 GOARCH=wasm GOTOOLCHAIN=go1.26.1 go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm . && ls -la plugin.wasm
cd ~/dev/goblog && go run . validate-plugin ~/dev/goblog-plugin-scholar/plugin.wasm
```
Expected `{"name":"scholar","display_name":"Scholar Publications","version":"2.0.0","runtime":"wasm"}`.

Live check in a local goblog (fresh sqlite; `.env` with `database=sqlite`, `sqlite_db=database.db`, `SESSION_KEY`, dummy `client_id`/`client_secret`): copy `plugin.wasm` to `plugins/wasm/scholar.wasm`, write the sidecar `plugins/wasm/scholar.json` = `{"allowed_hosts":["api.semanticscholar.org"]}`, start goblog, set settings via sqlite (`plugin_settings` rows for `scholar`: `enabled=true`, `semantic_scholar_id=1792904` — Jason's id used by goblog.live's scholar settings; if unsure use any public author id such as `1741101` (Oren Etzioni)), then `curl localhost:7000/research` → publications HTML with `citations`; second request is served from the cache (check the log has one fetch); `plugin_store` has an `articles` row. Record the transcript.

- [ ] **Step 4: Commit, push, PR**

```bash
git add -A && git commit -m "Add WASM exports, packaging and the release workflow

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git push -u origin feat/initial
gh pr create --base main --title "Scholar Publications as a WebAssembly plugin (v2.0.0)" --body "..."
```
After the maintainer merges: `gh release create v2.0.0 --title v2.0.0 --notes "..."`, watch the Release workflow, confirm the `plugin.wasm` asset; then open a PR to `goblogplatform/plugins` adding `  - repo: goblogplatform/goblog-plugin-scholar` to `registry.yaml` (its `Validate` runs the real check).
