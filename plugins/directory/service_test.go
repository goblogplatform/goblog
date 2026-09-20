package directory

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goblog/plugin"
	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// fakeRepo is one GitHub repository as the fake source presents it: a
// single vX.Y.Z release with plugin.wasm attached, or (theme: true) a
// theme's manifest, screenshot and archive.
type fakeRepo struct {
	theme   bool
	version string
	name    string // manifest + identity name
	stars   int
	readme  string
	wasm    []byte
	files   map[string]string // theme archive contents (templates/**, static/**)
}

type fakeSource struct {
	repos    map[string]*fakeRepo
	assets   atomic.Int32 // ReleaseAsset calls
	rendered atomic.Int32 // RenderMarkdown calls

	// hook, if set, runs once during ReleaseAsset (mid-build) and is then
	// cleared. It lets a test simulate another admin action racing a build
	// that is in flight, e.g. Delist.
	hook func()
}

func (f *fakeSource) repo(owner, repo string) (*fakeRepo, error) {
	r, ok := f.repos[owner+"/"+repo]
	if !ok {
		return nil, fmt.Errorf("%s/%s: no such repository", owner, repo)
	}
	return r, nil
}

func (f *fakeSource) Releases(_ context.Context, owner, repo string) ([]registry.Release, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	return []registry.Release{{
		Tag: "v" + r.version, Body: "notes", URL: "https://github.com/" + owner + "/" + repo + "/releases/tag/v" + r.version,
		PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		Assets: []registry.Asset{{ID: 1, Name: "plugin.wasm", Size: len(r.wasm),
			DownloadURL: "https://github.com/" + owner + "/" + repo + "/releases/download/v" + r.version + "/plugin.wasm"}},
	}}, nil
}

func (f *fakeSource) File(_ context.Context, owner, repo, ref, path string) ([]byte, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	if r.theme {
		switch path {
		case "goblog-theme.json":
			return []byte(`{"name":"` + r.name + `","display_name":"` + strings.ToUpper(r.name) + `","description":"A theme.","author":"Jason","license":"MIT","min_goblog_version":"0.5.0"}`), nil
		case "README.md":
			return []byte(r.readme), nil
		case "screenshot.png":
			return []byte("\x89PNG"), nil
		}
		return nil, registry.ErrNotFound
	}
	switch path {
	case "goblog-plugin.json":
		return []byte(`{"name":"` + r.name + `","display_name":"` + strings.ToUpper(r.name) + `","description":"Says hi.","author":"Jason","license":"MIT","runtime":"wasm","min_goblog_version":"0.3.0"}`), nil
	case "README.md":
		return []byte(r.readme), nil
	}
	return nil, registry.ErrNotFound
}

func (f *fakeSource) ReleaseAsset(_ context.Context, owner, repo string, _ int64) ([]byte, error) {
	f.assets.Add(1)
	if f.hook != nil {
		h := f.hook
		f.hook = nil
		h()
	}
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	return r.wasm, nil
}

func (f *fakeSource) RenderMarkdown(_ context.Context, _, md string) (string, error) {
	if md == "" {
		return "", nil
	}
	f.rendered.Add(1)
	return "<p>" + md + "</p>", nil
}

func (f *fakeSource) RepoStars(_ context.Context, owner, repo string) (int, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return 0, err
	}
	return r.stars, nil
}

// Zipball returns a theme repository's archive built from r.files, laid out
// the way GitHub's tag zipballs are: everything under one
// "<repo>-<version>/" folder.
func (f *fakeSource) Zipball(_ context.Context, owner, repo, ref string) ([]byte, error) {
	r, err := f.repo(owner, repo)
	if err != nil {
		return nil, err
	}
	if len(r.files) == 0 {
		return nil, errors.New("no archive")
	}
	return zipOf(repo+"-"+r.version+"/", r.files)
}

// zipOf builds a zip archive with the given entries under prefix — the
// small helper from registry's theme_test.go (test code in another
// package), minus its *testing.T since fakeSource.Zipball cannot take one.
func zipOf(prefix string, entries map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write([]byte(body)); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (f *fakeSource) FileURL(owner, repo, ref, path string) string {
	return "https://raw.test/" + owner + "/" + repo + "/" + ref + "/" + path
}

type fixture struct {
	db  *gorm.DB
	src *fakeSource
	val *registry.FakeValidator
	tv  *registry.FakeThemeValidator
	svc *Service
}

// newFixture returns a Service over sqlite with o/hello (v1.0.0, name
// "hello"), o/zeta (v0.1.0, name "zeta") and o/ocean (a theme, v1.0.0, name
// "ocean") available at the fake GitHub.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	hello := &fakeRepo{version: "1.0.0", name: "hello", stars: 7, readme: "# Hello", wasm: []byte("\x00asm hello")}
	zeta := &fakeRepo{version: "0.1.0", name: "zeta", stars: 1, readme: "# Zeta", wasm: []byte("\x00asm zeta")}
	ocean := &fakeRepo{theme: true, version: "1.0.0", name: "ocean", stars: 3, readme: "# Ocean", files: map[string]string{"templates/home.html": "home"}}
	src := &fakeSource{repos: map[string]*fakeRepo{"o/hello": hello, "o/zeta": zeta, "o/ocean": ocean}}
	val := &registry.FakeValidator{Infos: map[string]plugin.Info{
		registry.Sum(hello.wasm): {Name: "hello", DisplayName: "Hello", Version: "1.0.0", Runtime: "wasm"},
		registry.Sum(zeta.wasm):  {Name: "zeta", DisplayName: "Zeta", Version: "0.1.0", Runtime: "wasm"},
	}}
	tv := &registry.FakeThemeValidator{}
	db := testDB(t)
	svc := NewService(db, func(string) registry.Source { return src }, val, tv, func() string { return "https://example.test" })
	return &fixture{db: db, src: src, val: val, tv: tv, svc: svc}
}

func (f *fixture) indexNames(t *testing.T, kind string) []string {
	t.Helper()
	_, entries := f.svc.Index(kind)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

func TestService_SubmitThenApprove(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Submit(context.Background(), KindPlugin, "https://github.com/o/hello", "1.1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusPending || r.SubmitterIP != "1.1.1.1" || r.Repo != "o/hello" {
		t.Errorf("repo = %+v", r)
	}
	var b Build
	if err := f.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil || b.Name != "hello" || b.Version != "1.0.0" || b.Stars != 7 {
		t.Errorf("build = %+v %v", b, err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 0 {
		t.Errorf("pending must not be in the index: %v", names)
	}
	if _, ok := f.svc.Detail(KindPlugin, "hello"); ok {
		t.Error("pending must not have a public detail")
	}
	if err := f.svc.Approve(r.ID); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 1 || names[0] != "hello" {
		t.Errorf("approved should be in the index: %v", names)
	}
	d, ok := f.svc.Detail(KindPlugin, "hello")
	if !ok || d.DetailURL != "https://example.test/plugins/hello.json" || d.ReadmeHTML != "<p># Hello</p>" {
		t.Errorf("detail = %+v %v", d, ok)
	}
	raw, _ := f.svc.Index(KindPlugin)
	if !strings.Contains(string(raw), `"name": "hello"`) {
		t.Errorf("raw index = %s", raw)
	}
}

func TestService_SubmitDuplicatesAndResubmit(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.Submit(context.Background(), KindPlugin, "o/hello", "ip", "")
	if _, err := f.svc.Submit(context.Background(), KindPlugin, "O/Hello", "ip", ""); !errors.Is(err, ErrUnderReview) {
		t.Errorf("pending again: %v", err)
	}
	f.svc.Approve(r.ID)
	if _, err := f.svc.Submit(context.Background(), KindPlugin, "o/hello", "ip", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("approved again: %v", err)
	}
	if err := f.svc.Reject(r.ID, "nope"); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 0 {
		t.Errorf("rejected must leave the index: %v", names)
	}
	f.src.repos["o/hello"].version = "1.1.0"
	f.val.Infos[registry.Sum(f.src.repos["o/hello"].wasm)] = plugin.Info{Name: "hello", Version: "1.1.0", Runtime: "wasm"}
	again, err := f.svc.Submit(context.Background(), KindPlugin, "o/hello", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != r.ID || again.Status != StatusPending || again.RejectReason != "" || again.DecidedAt != nil {
		t.Errorf("resubmission should reset the same row: %+v", again)
	}
	var b Build
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.Version != "1.1.0" {
		t.Errorf("resubmission should rebuild: %+v", b)
	}
}

func TestService_SubmitErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, KindPlugin, "not a repo", "ip", ""); !errors.Is(err, ErrBadRepo) {
		t.Errorf("bad input: %v", err)
	}
	var ve *ValidationError
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/missing", "ip", ""); !errors.As(err, &ve) || !strings.Contains(ve.Msg, "no such repository") {
		t.Errorf("unknown repo should be a ValidationError: %v", err)
	}
	var n int64
	f.db.Model(&Repo{}).Count(&n)
	if n != 0 {
		t.Errorf("failed submissions must not leave rows, got %d", n)
	}

	// Name collision: o/zeta re-using hello's plugin name.
	f.svc.Add(ctx, KindPlugin, "o/hello", "")
	f.src.repos["o/zeta"].name = "hello"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "hello", Version: "0.1.0", Runtime: "wasm"}
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/zeta", "ip", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
}

func TestService_SubmitRateLimitAndBusy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		f.svc.Submit(ctx, KindPlugin, "o/missing", "9.9.9.9", "") // fails validation but counts
	}
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/hello", "9.9.9.9", ""); !errors.Is(err, ErrRateLimited) {
		t.Errorf("6th attempt: %v", err)
	}
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/hello", "8.8.8.8", ""); err != nil {
		t.Errorf("other address is fine: %v", err)
	}

	f.svc.validate.Lock()
	defer f.svc.validate.Unlock()
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/zeta", "7.7.7.7", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("while a validation runs: %v", err)
	}
}

func TestService_AddDelistNotFound(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Add(context.Background(), KindPlugin, "https://github.com/o/hello", "")
	if err != nil || r.Status != StatusApproved || r.DecidedAt == nil {
		t.Fatalf("add: %+v %v", r, err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 1 {
		t.Errorf("index after add: %v", names)
	}
	if _, err := f.svc.Add(context.Background(), KindPlugin, "o/hello", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("add twice: %v", err)
	}
	if err := f.svc.Delist(r.ID); err != nil {
		t.Fatal(err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 0 {
		t.Errorf("index after delist: %v", names)
	}
	var n int64
	f.db.Model(&Build{}).Count(&n)
	if n != 0 {
		t.Error("delist must remove the build")
	}
	for _, err := range []error{f.svc.Approve(999), f.svc.Reject(999, ""), f.svc.Delist(999), f.svc.Rebuild(context.Background(), 999, "")} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown id: %v", err)
		}
	}
}

func TestService_RebuildKeepsOldDocOnFailure(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.src.repos["o/hello"].readme = ""
	delete(f.src.repos, "o/hello")
	err := f.svc.Rebuild(context.Background(), r.ID, "")
	if err == nil {
		t.Fatal("rebuild of a vanished repo must fail")
	}
	var b Build
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.Version != "1.0.0" || b.LastError == "" || b.LastAttemptAt == nil {
		t.Errorf("build after failure = %+v", b)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 1 {
		t.Errorf("still listed: %v", names)
	}
	f.src.repos["o/hello"] = &fakeRepo{version: "1.0.0", name: "hello", stars: 8, readme: "# Hello", wasm: []byte("\x00asm hello")}
	if err := f.svc.Rebuild(context.Background(), r.ID, ""); err != nil {
		t.Fatal(err)
	}
	f.db.Where("repo_id = ?", r.ID).First(&b)
	if b.LastError != "" || b.Stars != 8 {
		t.Errorf("build after success = %+v", b)
	}
}

func TestService_RefreshAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hello, _ := f.svc.Add(ctx, KindPlugin, "o/hello", "")
	f.svc.Add(ctx, KindPlugin, "o/zeta", "")
	old := time.Now().Add(-8 * 24 * time.Hour)
	f.db.Model(&Repo{}).Where("id = ?", hello.ID).Updates(map[string]any{"submitter_ip": "1.2.3.4", "submitted_at": old})

	f.src.repos["o/hello"].stars = 70 // unchanged release: stars only
	f.src.repos["o/zeta"].version = "0.2.0"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "zeta", Version: "0.2.0", Runtime: "wasm"}
	assetsBefore := f.src.assets.Load()
	if err := f.svc.RefreshAll(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if f.src.assets.Load()-assetsBefore != 1 {
		t.Errorf("only zeta (new release) should be downloaded, got %d downloads", f.src.assets.Load()-assetsBefore)
	}
	_, entries := f.svc.Index(KindPlugin)
	if len(entries) != 2 || entries[0].Name != "hello" || entries[0].Stars != 70 || entries[0].Version != "1.0.0" || entries[1].Version != "0.2.0" {
		t.Errorf("index after refresh = %+v", entries)
	}
	var r Repo
	f.db.First(&r, hello.ID)
	if r.SubmitterIP != "" {
		t.Error("addresses older than 7 days must be blanked")
	}
	if f.svc.LastRefresh().IsZero() {
		t.Error("LastRefresh must be stamped")
	}

	// A failing repo does not stop the others and is reported.
	delete(f.src.repos, "o/hello")
	f.src.repos["o/zeta"].stars = 5
	err := f.svc.RefreshAll(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "o/hello") {
		t.Errorf("want an error naming o/hello, got %v", err)
	}
	_, entries = f.svc.Index(KindPlugin)
	if len(entries) != 2 || entries[1].Stars != 5 {
		t.Errorf("zeta should still refresh: %+v", entries)
	}
}

func TestService_ListAndGet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.Add(ctx, KindPlugin, "o/hello", "")
	z, _ := f.svc.Submit(ctx, KindPlugin, "o/zeta", "ip", "")
	all, err := f.svc.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	pending, _ := f.svc.List(StatusPending)
	if len(pending) != 1 || pending[0].Repo != "o/zeta" || pending[0].Name != "zeta" || pending[0].Version != "0.1.0" || pending[0].SourceURL != "https://github.com/o/zeta" {
		t.Errorf("pending = %+v", pending)
	}
	v, d, err := f.svc.Get(z.ID)
	if err != nil || v.ID != z.ID || d.ReadmeHTML != "<p># Zeta</p>" {
		t.Errorf("get = %+v %+v %v", v, d, err)
	}
	if _, _, err := f.svc.Get(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get unknown: %v", err)
	}
}

// TestService_RebuildDelistRaceLeavesNoOrphanBuild: Delist takes no lock
// rebuildRepo holds, so it can delete a repository and its build while a
// rebuild's network round trip is in flight. rebuildRepo must not resurrect
// the deleted build (Build.Name is uniqueIndex, so an orphan would
// permanently block re-adding the same repository).
func TestService_RebuildDelistRaceLeavesNoOrphanBuild(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, err := f.svc.Add(ctx, KindPlugin, "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}

	f.src.hook = func() {
		if err := f.svc.Delist(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Rebuild(ctx, r.ID, ""); err == nil || !strings.Contains(err.Error(), "delisted during rebuild") {
		t.Errorf("rebuild racing a delist should fail naming the race, got %v", err)
	}

	var n int64
	f.db.Model(&Build{}).Count(&n)
	if n != 0 {
		t.Errorf("delist must win the race: no orphan build, got %d rows", n)
	}
	f.db.Model(&Repo{}).Count(&n)
	if n != 0 {
		t.Errorf("delist must win the race: no orphan repo, got %d rows", n)
	}

	if _, err := f.svc.Add(ctx, KindPlugin, "o/hello", ""); err != nil {
		t.Errorf("re-adding after the race should succeed, got %v", err)
	}
}

// TestService_RebuildNameCollisionKeepsOldBuildAndRecordsError: a plugin
// that renames itself onto a name another listed repository already
// publishes must not corrupt the losing build's row or surface a raw DB
// error — it is a curation refusal like any other ErrNameTaken.
func TestService_RebuildNameCollisionKeepsOldBuildAndRecordsError(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.Add(ctx, KindPlugin, "o/hello", "")
	z, err := f.svc.Add(ctx, KindPlugin, "o/zeta", "")
	if err != nil {
		t.Fatal(err)
	}

	f.src.repos["o/zeta"].name = "hello"
	f.src.repos["o/zeta"].version = "0.2.0"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "hello", Version: "0.2.0", Runtime: "wasm"}

	if err := f.svc.Rebuild(ctx, z.ID, ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rebuild onto a taken name: %v", err)
	}

	var b Build
	if err := f.db.Where("repo_id = ?", z.ID).First(&b).Error; err != nil {
		t.Fatal(err)
	}
	if b.Name != "zeta" || b.Version != "0.1.0" || b.LastError != ErrNameTaken.Error() || b.LastAttemptAt == nil {
		t.Errorf("build after collision = %+v", b)
	}

	if names := f.indexNames(t, KindPlugin); len(names) != 2 {
		t.Errorf("both repositories should still be listed: %v", names)
	}
}

func TestService_ThemesAreSeparateFromPlugins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Add(ctx, "bogus", "o/hello", ""); !errors.Is(err, ErrBadKind) {
		t.Errorf("bad kind: %v", err)
	}
	if _, err := f.svc.Add(ctx, KindPlugin, "o/hello", ""); err != nil {
		t.Fatal(err)
	}
	// A theme repo whose manifest name is "hello" too.
	f.src.repos["o/ocean"].name = "hello"
	th, err := f.svc.Add(ctx, KindTheme, "o/ocean", "")
	if err != nil || th.Kind != KindTheme {
		t.Fatalf("add theme: %+v %v", th, err)
	}
	if names := f.indexNames(t, KindPlugin); len(names) != 1 || names[0] != "hello" {
		t.Errorf("plugin index = %v", names)
	}
	if names := f.indexNames(t, KindTheme); len(names) != 1 || names[0] != "hello" {
		t.Errorf("theme index = %v", names)
	}
	d, ok := f.svc.Detail(KindTheme, "hello")
	if !ok || d.Kind != KindTheme || d.InstallType != "theme" || d.ScreenshotURL == "" {
		t.Errorf("theme detail = %+v %v", d, ok)
	}
	if d, _ := f.svc.Detail(KindPlugin, "hello"); d.Kind != KindPlugin {
		t.Errorf("plugin detail = %+v", d)
	}
	// Same kind, same name, different repo: refused.
	f.src.repos["o/zeta"].name = "hello"
	f.val.Infos[registry.Sum(f.src.repos["o/zeta"].wasm)] = plugin.Info{Name: "hello", Version: "0.1.0", Runtime: "wasm"}
	if _, err := f.svc.Submit(ctx, KindPlugin, "o/zeta", "ip", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("same-kind collision: %v", err)
	}
	// A repo keeps its kind: resubmitting o/hello as a theme is a duplicate, not a new theme.
	if _, err := f.svc.Submit(ctx, KindTheme, "o/hello", "ip", ""); !errors.Is(err, ErrAlreadyListed) {
		t.Errorf("repo already listed under another kind: %v", err)
	}
	views, _ := f.svc.List("")
	kinds := map[string]string{}
	for _, v := range views {
		kinds[v.Repo] = v.Kind
	}
	if kinds["o/hello"] != KindPlugin || kinds["o/ocean"] != KindTheme {
		t.Errorf("List kinds = %v", kinds)
	}
	if err := f.svc.RefreshAll(ctx, ""); err != nil {
		t.Errorf("refresh with both kinds: %v", err)
	}
}
