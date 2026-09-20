package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves the REST endpoints GitHubSource uses.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"tag_name":"v1.1.0","name":"v1.1.0","body":"Second","draft":false,"prerelease":false,"published_at":"2026-09-15T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.1.0",
		   "assets":[{"id":11,"name":"plugin.wasm","size":4,"browser_download_url":"https://github.com/o/r/releases/download/v1.1.0/plugin.wasm"}]},
		  {"tag_name":"v1.2.0-rc1","name":"rc","body":"","draft":false,"prerelease":true,"published_at":"2026-09-16T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.2.0-rc1"},
		  {"tag_name":"v1.0.0","name":"v1.0.0","body":"First","draft":false,"prerelease":false,"published_at":"2026-09-14T00:00:00Z","html_url":"https://github.com/o/r/releases/tag/v1.0.0"}
		]`))
	})
	// DownloadReleaseAsset GETs the API asset path with Accept:
	// application/octet-stream and streams the body on a 200 (no redirect).
	mux.HandleFunc("GET /repos/o/r/releases/assets/11", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "expected Accept: application/octet-stream", 400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("wasm"))
	})
	// Asset 12 is one byte over the limit; ReleaseAsset must refuse it
	// without reading it all into memory first.
	mux.HandleFunc("GET /repos/o/r/releases/assets/12", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.CopyN(w, zeroReader{}, MaxAssetBytes+1)
	})
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"full_name":"o/r","stargazers_count":42}`))
	})
	mux.HandleFunc("GET /repos/o/r/contents/plugin.go", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "v1.1.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"file","encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte("package main\n")) + `"}`))
	})
	mux.HandleFunc("POST /markdown", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text, Mode, Context string }
		if err := jsonDecode(r, &body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Mode != "gfm" || body.Context != "o/r" {
			http.Error(w, "expected gfm mode with repo context", 400)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<p>" + body.Text + "</p>"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubSource(t *testing.T) {
	srv := fakeGitHub(t)
	src := NewGitHubSource("", srv.URL)
	ctx := context.Background()

	rels, err := src.Releases(ctx, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 3 || rels[0].Tag != "v1.1.0" || rels[1].Prerelease != true || rels[2].Body != "First" || rels[0].URL == "" || rels[0].PublishedAt.IsZero() {
		t.Errorf("releases = %+v", rels)
	}
	if len(rels[0].Assets) != 1 || rels[0].Assets[0].ID != 11 || rels[0].Assets[0].Name != "plugin.wasm" || rels[0].Assets[0].Size != 4 ||
		rels[0].Assets[0].DownloadURL != "https://github.com/o/r/releases/download/v1.1.0/plugin.wasm" {
		t.Errorf("assets = %+v", rels[0].Assets)
	}
	if len(rels[1].Assets) != 0 {
		t.Errorf("a release without assets should have none, got %+v", rels[1].Assets)
	}

	asset, err := src.ReleaseAsset(ctx, "o", "r", 11)
	if err != nil || string(asset) != "wasm" {
		t.Errorf("ReleaseAsset: %q %v", asset, err)
	}
	if _, err := src.ReleaseAsset(ctx, "o", "r", 99); err == nil {
		t.Error("ReleaseAsset with an unknown id should fail")
	}
	if _, err := src.ReleaseAsset(ctx, "o", "r", 12); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("an asset over MaxAssetBytes should be refused, got %v", err)
	}

	b, err := src.File(ctx, "o", "r", "v1.1.0", "plugin.go")
	if err != nil || string(b) != "package main\n" {
		t.Errorf("File: %q %v", b, err)
	}
	if _, err := src.File(ctx, "o", "r", "v9.9.9", "plugin.go"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing ref should be ErrNotFound, got %v", err)
	}
	if _, err := src.File(ctx, "o", "r", "v1.1.0", "CHANGELOG.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file should be ErrNotFound, got %v", err)
	}

	html, err := src.RenderMarkdown(ctx, "o/r", "hi")
	if err != nil || !strings.Contains(html, "<p>hi</p>") {
		t.Errorf("RenderMarkdown: %q %v", html, err)
	}
	if html, err := src.RenderMarkdown(ctx, "o/r", ""); err != nil || html != "" {
		t.Errorf("empty markdown should render to empty string without a request, got %q %v", html, err)
	}

	stars, err := src.RepoStars(ctx, "o", "r")
	if err != nil || stars != 42 {
		t.Errorf("RepoStars = %d, %v", stars, err)
	}
	if _, err := src.RepoStars(ctx, "o", "missing"); err == nil {
		t.Error("RepoStars on an unknown repo should fail")
	}
}

func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// zeroReader is an endless stream of zero bytes.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestGitHubSource_SendsTokenAndUserAgent(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"stargazers_count":1}`))
	}))
	t.Cleanup(srv.Close)
	src := NewGitHubSource("tok", srv.URL)
	src.SetUserAgent("goblog-directory/test")
	if _, err := src.RepoStars(context.Background(), "o", "r"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotUA != "goblog-directory/test" {
		t.Errorf("headers: auth=%q ua=%q", gotAuth, gotUA)
	}
}

func TestGitHubSource_RateLimitIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	src := NewGitHubSource("", srv.URL)
	_, err := src.Releases(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "github_token") {
		t.Errorf("want a rate-limit error naming github_token, got %v", err)
	}
}
