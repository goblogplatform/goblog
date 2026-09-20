package admin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"goblog/plugins/directory"
	"goblog/plugins/directory/registry"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubSource is an in-memory registry.Source with one repository, o/hello
// v1.0.0, whose plugin.wasm the FakeValidator below recognises.
type stubSource struct{ missing bool }

var helloWasm = []byte("\x00asm hello")

func (s stubSource) Releases(_ context.Context, owner, repo string) ([]registry.Release, error) {
	if s.missing {
		return nil, fmt.Errorf("%s/%s: no such repository", owner, repo)
	}
	switch owner + "/" + repo {
	case "o/hello":
		return []registry.Release{{Tag: "v1.0.0", Body: "notes", URL: "https://github.com/o/hello/releases/tag/v1.0.0",
			PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
			Assets:      []registry.Asset{{ID: 1, Name: "plugin.wasm", Size: len(helloWasm), DownloadURL: "https://github.com/o/hello/releases/download/v1.0.0/plugin.wasm"}}}}, nil
	case "o/ocean":
		return []registry.Release{{Tag: "v1.0.0", Body: "notes", URL: "https://github.com/o/ocean/releases/tag/v1.0.0",
			PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}}, nil
	}
	return nil, fmt.Errorf("%s/%s: no such repository", owner, repo)
}
func (s stubSource) File(_ context.Context, owner, repo, _, path string) ([]byte, error) {
	if owner+"/"+repo == "o/ocean" {
		switch path {
		case "goblog-theme.json":
			return []byte(`{"name":"ocean","display_name":"Ocean","description":"Blue.","author":"Jason","license":"MIT","min_goblog_version":"0.5.0"}`), nil
		case "README.md":
			return []byte("# Ocean"), nil
		case "screenshot.png":
			return []byte("\x89PNG"), nil
		}
		return nil, registry.ErrNotFound
	}
	switch path {
	case "goblog-plugin.json":
		return []byte(`{"name":"hello","display_name":"Hello","description":"Says hi.","author":"Jason","license":"MIT","runtime":"wasm","min_goblog_version":"0.3.0"}`), nil
	case "README.md":
		return []byte("# Hello"), nil
	}
	return nil, registry.ErrNotFound
}
func (s stubSource) ReleaseAsset(context.Context, string, string, int64) ([]byte, error) {
	return helloWasm, nil
}
func (s stubSource) RenderMarkdown(_ context.Context, _, md string) (string, error) {
	return "<p>" + md + "</p>", nil
}
func (s stubSource) RepoStars(context.Context, string, string) (int, error) { return 7, nil }

// Zipball serves o/ocean's theme archive (one template, laid out the way
// GitHub's tag zipballs are: everything under "<repo>-<version>/"); every
// other repository has no archive, matching a plugin repo's Source.
func (s stubSource) Zipball(_ context.Context, owner, repo, _ string) ([]byte, error) {
	if owner+"/"+repo != "o/ocean" {
		return nil, fmt.Errorf("no archive")
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("ocean-1.0.0/templates/home.html")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write([]byte("home")); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s stubSource) FileURL(owner, repo, ref, path string) string {
	return "https://raw.test/" + owner + "/" + repo + "/" + ref + "/" + path
}

type directoryHarness struct {
	router *gin.Engine
	auth   *Auth
	db     *gorm.DB
	dir    *directory.Plugin
	src    *stubSource
}

func newDirectoryHarness(t *testing.T, wire bool) *directoryHarness {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	src := &stubSource{}
	dir := directory.New()
	dir.SetSource(func(string) registry.Source { return src })
	dir.SetValidator(&registry.FakeValidator{Infos: map[string]plugin.Info{
		registry.Sum(helloWasm): {Name: "hello", DisplayName: "Hello", Version: "1.0.0", Runtime: "wasm"},
	}})
	dir.SetThemeValidator(&registry.FakeThemeValidator{})
	if wire {
		if err := dir.OnInit(db); err != nil {
			t.Fatal(err)
		}
		ad.Directory = dir
	}

	router := gin.New()
	router.GET("/api/v1/directory/repos", ad.ListDirectoryRepos)
	router.POST("/api/v1/directory/repos", ad.AddDirectoryRepo)
	router.GET("/api/v1/directory/repos/:id", ad.GetDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/approve", ad.ApproveDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/reject", ad.RejectDirectoryRepo)
	router.POST("/api/v1/directory/repos/:id/rebuild", ad.RebuildDirectoryRepo)
	router.DELETE("/api/v1/directory/repos/:id", ad.DelistDirectoryRepo)
	return &directoryHarness{router: router, auth: a, db: db, dir: dir, src: src}
}

func (h *directoryHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

var directoryRoutes = []struct{ m, p string }{
	{"GET", "/api/v1/directory/repos"}, {"POST", "/api/v1/directory/repos"}, {"GET", "/api/v1/directory/repos/1"},
	{"POST", "/api/v1/directory/repos/1/approve"}, {"POST", "/api/v1/directory/repos/1/reject"},
	{"POST", "/api/v1/directory/repos/1/rebuild"}, {"DELETE", "/api/v1/directory/repos/1"},
}

func TestDirectoryAPI_NonAdmin(t *testing.T) {
	h := newDirectoryHarness(t, true)
	h.auth.On("IsAdmin", mock.Anything).Return(false)
	for _, r := range directoryRoutes {
		if w := h.do(r.m, r.p, `{"repo":"o/hello"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401, got %d", r.m, r.p, w.Code)
		}
	}
}

func TestDirectoryAPI_NotHosted(t *testing.T) {
	h := newDirectoryHarness(t, false)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	for _, r := range directoryRoutes {
		if w := h.do(r.m, r.p, `{"repo":"o/hello"}`); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503, got %d", r.m, r.p, w.Code)
		}
	}
}

func TestDirectoryAPI_Lifecycle(t *testing.T) {
	h := newDirectoryHarness(t, true)
	h.auth.On("IsAdmin", mock.Anything).Return(true)

	w := h.do("POST", "/api/v1/directory/repos", `{"repo":"https://github.com/o/hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var added directory.Repo
	json.Unmarshal(w.Body.Bytes(), &added)
	if added.Status != directory.StatusApproved || added.ID == 0 {
		t.Fatalf("added = %+v", added)
	}
	id := fmt.Sprint(added.ID)

	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/hello"}`); w.Code != http.StatusConflict {
		t.Errorf("add twice: %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"nope"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad repo: %d", w.Code)
	}
	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/other"}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "no such repository") {
		t.Errorf("validation failure: %d %s", w.Code, w.Body.String())
	}
	if w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/hello","kind":"bogus"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad kind: %d %s", w.Code, w.Body.String())
	}

	w = h.do("POST", "/api/v1/directory/repos", `{"repo":"o/ocean","kind":"theme"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kind":"theme"`) {
		t.Fatalf("add theme: %d %s", w.Code, w.Body.String())
	}

	w = h.do("GET", "/api/v1/directory/repos?status=approved", "")
	var list []directory.RepoView
	json.Unmarshal(w.Body.Bytes(), &list)
	byName := map[string]directory.RepoView{}
	for _, r := range list {
		byName[r.Name] = r
	}
	if w.Code != http.StatusOK || len(list) != 2 || byName["hello"].Kind != directory.KindPlugin || byName["hello"].Stars != 7 {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	if byName["ocean"].Kind != directory.KindTheme || byName["ocean"].ScreenshotURL == "" {
		t.Errorf("theme row: %+v", byName["ocean"])
	}

	w = h.do("GET", "/api/v1/directory/repos/"+id, "")
	var got struct {
		Repo   directory.RepoView `json:"repo"`
		Detail registry.DetailDoc `json:"detail"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != http.StatusOK || got.Repo.ID != added.ID || got.Detail.ReadmeHTML != "<p># Hello</p>" {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}

	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/reject", `{"reason":"not yet"}`); w.Code != http.StatusOK {
		t.Errorf("reject: %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/api/v1/directory/repos?status=rejected", "")
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].RejectReason != "not yet" {
		t.Errorf("rejected list: %s", w.Body.String())
	}
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/approve", ""); w.Code != http.StatusOK {
		t.Errorf("approve: %d", w.Code)
	}
	if raw, _ := h.dir.Service().Index(directory.KindPlugin); !strings.Contains(string(raw), `"name": "hello"`) {
		t.Errorf("approve must regenerate the index: %s", raw)
	}

	h.src.missing = true
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/rebuild", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("rebuild failure: %d %s", w.Code, w.Body.String())
	}
	h.src.missing = false
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/rebuild", ""); w.Code != http.StatusOK {
		t.Errorf("rebuild: %d %s", w.Code, w.Body.String())
	}

	if w := h.do("DELETE", "/api/v1/directory/repos/"+id, ""); w.Code != http.StatusOK {
		t.Errorf("delist: %d", w.Code)
	}
	for _, r := range directoryRoutes[2:] {
		if w := h.do(r.m, strings.Replace(r.p, "/1", "/"+id, 1), `{}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s after delist: expected 404, got %d", r.m, r.p, w.Code)
		}
	}
	if w := h.do("GET", "/api/v1/directory/repos/abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id: %d", w.Code)
	}
}

// TestDirectoryAPI_RejectEmptyAndMalformedBody guards against a regression
// where RejectDirectoryRepo used c.BindJSON, which writes its own 400 to the
// response on a decode failure; the handler then went on to write 200 on
// top of it, and an *empty* body (meant to mean "no reason") counted as such
// a decode failure. ShouldBindJSON must be used instead, and only a
// malformed *non-empty* body should be rejected as bad input.
func TestDirectoryAPI_RejectEmptyAndMalformedBody(t *testing.T) {
	h := newDirectoryHarness(t, true)
	h.auth.On("IsAdmin", mock.Anything).Return(true)

	w := h.do("POST", "/api/v1/directory/repos", `{"repo":"o/hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var added directory.Repo
	json.Unmarshal(w.Body.Bytes(), &added)
	id := fmt.Sprint(added.ID)

	// An empty body is a rejection without a reason, not a broken request.
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/reject", ""); w.Code != http.StatusOK {
		t.Fatalf("reject with empty body: %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/api/v1/directory/repos?status=rejected", "")
	var list []directory.RepoView
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].RejectReason != "" {
		t.Fatalf("after empty-body reject: %s", w.Body.String())
	}

	// A malformed non-empty body is still a 400, and must not change the
	// repository's state.
	if w := h.do("POST", "/api/v1/directory/repos/"+id+"/reject", "{not json"); w.Code != http.StatusBadRequest {
		t.Errorf("reject with malformed body: expected 400, got %d %s", w.Code, w.Body.String())
	}
	w = h.do("GET", "/api/v1/directory/repos?status=rejected", "")
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].RejectReason != "" {
		t.Errorf("status must be unchanged after malformed reject body: %s", w.Body.String())
	}
}

func TestPluginStatus_ReportsDirectoryHosted(t *testing.T) {
	h := newPluginsHarness(t)
	h.auth.On("IsAdmin", mock.Anything).Return(true)
	dir := directory.New()
	dir.OnInit(h.db)
	h.ad.Directory = dir
	w := h.do("GET", "/api/v1/plugins/status", "")
	if !strings.Contains(w.Body.String(), `"directory_hosted":false`) {
		t.Errorf("not enabled: %s", w.Body.String())
	}
	h.db.Create(&plugin.PluginSetting{PluginName: "directory", Key: "enabled", Value: "true"})
	w = h.do("GET", "/api/v1/plugins/status", "")
	if !strings.Contains(w.Body.String(), `"directory_hosted":true`) {
		t.Errorf("enabled: %s", w.Body.String())
	}
}
