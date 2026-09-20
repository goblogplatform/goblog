package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"goblog/plugin"
)

// memSource is an in-memory Source for validator/builder tests.
type memSource struct {
	releases map[string][]Release // "owner/repo" → releases
	files    map[string]string    // "owner/repo@ref:path" → content
	assets   map[int64][]byte     // asset id → bytes
	rendered int                  // RenderMarkdown call count
	stars    map[string]int       // "owner/repo" → stargazers_count
	starsErr error                // when set, RepoStars fails for every repo
}

func (m *memSource) Releases(_ context.Context, owner, repo string) ([]Release, error) {
	rels, ok := m.releases[owner+"/"+repo]
	if !ok {
		return nil, errors.New("no such repo")
	}
	return rels, nil
}

func (m *memSource) File(_ context.Context, owner, repo, ref, path string) ([]byte, error) {
	if c, ok := m.files[owner+"/"+repo+"@"+ref+":"+path]; ok {
		return []byte(c), nil
	}
	return nil, ErrNotFound
}

func (m *memSource) ReleaseAsset(_ context.Context, owner, repo string, id int64) ([]byte, error) {
	if b, ok := m.assets[id]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%s/%s: no asset %d", owner, repo, id)
}

func (m *memSource) RenderMarkdown(_ context.Context, ownerRepo, md string) (string, error) {
	if md == "" {
		return "", nil
	}
	m.rendered++
	return "<p>" + md + "</p>", nil
}

func (m *memSource) RepoStars(_ context.Context, owner, repo string) (int, error) {
	if m.starsErr != nil {
		return 0, m.starsErr
	}
	if n, ok := m.stars[owner+"/"+repo]; ok {
		return n, nil
	}
	return 0, nil
}

// helloWasm stands in for the plugin.wasm asset attached to hello v1.1.0.
var helloWasm = []byte("\x00asm hello v1.1.0")

// helloAsset is the release asset the validator downloads for hello v1.1.0.
var helloAsset = Asset{ID: 11, Name: "plugin.wasm", Size: len(helloWasm), DownloadURL: "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm"}

func helloSource() *memSource {
	return &memSource{
		releases: map[string][]Release{
			"o/hello": {
				{Tag: "v1.1.0", Body: "Second", URL: "https://github.com/o/hello/releases/tag/v1.1.0", PublishedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), Assets: []Asset{helloAsset}},
				{Tag: "v2.0.0-rc1", Prerelease: true, PublishedAt: time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)},
				{Tag: "v3.0.0", Draft: true, PublishedAt: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)},
				{Tag: "v1.0.0", Body: "First", URL: "https://github.com/o/hello/releases/tag/v1.0.0", PublishedAt: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)},
			},
		},
		files: map[string]string{
			"o/hello@v1.1.0:goblog-plugin.json": goodManifest,
			"o/hello@v1.1.0:README.md":          "# Hello",
			"o/hello@v1.1.0:CHANGELOG.md":       "## 1.1.0\n- second",
		},
		assets: map[int64][]byte{11: helloWasm},
		stars:  map[string]int{"o/hello": 7},
	}
}

func helloValidator() *FakeValidator {
	return &FakeValidator{Infos: map[string]plugin.Info{Sum(helloWasm): {Name: "hello", DisplayName: "Hello", Version: "1.1.0", Runtime: "wasm"}}}
}

func TestValidateEntry_Good(t *testing.T) {
	v, err := ValidateEntry(context.Background(), helloSource(), helloValidator(), "o/hello")
	if err != nil {
		t.Fatal(err)
	}
	if v.Owner != "o" || v.Name != "hello" || v.Manifest.Name != "hello" || v.Release.Tag != "v1.1.0" || v.Version != "1.1.0" {
		t.Errorf("validated = %+v", v)
	}
	if len(v.Releases) != 2 || v.Releases[0].Tag != "v1.1.0" || v.Releases[1].Tag != "v1.0.0" {
		t.Errorf("releases should exclude drafts/prereleases, newest first: %+v", v.Releases)
	}
	if string(v.Entry) != string(helloWasm) || v.SHA256 != Sum(helloWasm) {
		t.Errorf("entry/sha mismatch: entry=%q sha=%s", v.Entry, v.SHA256)
	}
	if v.Asset != helloAsset || v.Asset.DownloadURL != "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm" {
		t.Errorf("asset = %+v, want %+v", v.Asset, helloAsset)
	}
}

func TestValidateEntry_FiltersHistoryByTagPattern(t *testing.T) {
	src := helloSource()
	src.releases["o/hello"] = append(src.releases["o/hello"], Release{
		Tag: "weird-tag", Body: "old", URL: "https://github.com/o/hello/releases/tag/weird-tag",
		PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	v, err := ValidateEntry(context.Background(), src, helloValidator(), "o/hello")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Releases {
		if r.Tag == "weird-tag" {
			t.Errorf("releases should exclude tags that don't match vX.Y.Z: %+v", v.Releases)
		}
	}
	if len(v.Releases) != 2 {
		t.Errorf("releases = %+v", v.Releases)
	}
}

func TestValidateEntry_PicksLatestByDate(t *testing.T) {
	src := helloSource()
	// GitHub order is not trusted: put the older release first.
	rels := src.releases["o/hello"]
	src.releases["o/hello"] = []Release{rels[3], rels[0]}
	v, err := ValidateEntry(context.Background(), src, helloValidator(), "o/hello")
	if err != nil {
		t.Fatal(err)
	}
	if v.Release.Tag != "v1.1.0" {
		t.Errorf("latest should be v1.1.0 by published date, got %s", v.Release.Tag)
	}
}

func TestValidateEntry_Errors(t *testing.T) {
	type tc struct {
		mutate func(s *memSource, f *FakeValidator)
		want   string
	}
	cases := map[string]tc{
		"bad repo string": {func(s *memSource, f *FakeValidator) {}, "owner/name"},
		"no releases": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"] = []Release{{Tag: "v9.0.0", Draft: true}}
		}, "no published release"},
		"tag not semver": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"][0].Tag = "1.1.0"
			s.files["o/hello@1.1.0:goblog-plugin.json"] = goodManifest
		}, "vX.Y.Z"},
		"missing manifest": {func(s *memSource, f *FakeValidator) {
			delete(s.files, "o/hello@v1.1.0:goblog-plugin.json")
		}, "goblog-plugin.json"},
		"invalid manifest": {func(s *memSource, f *FakeValidator) {
			s.files["o/hello@v1.1.0:goblog-plugin.json"] = `{"name":"Bad"}`
		}, "goblog-plugin.json"},
		"no asset": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"][0].Assets = nil
		}, "no asset named plugin.wasm"},
		"wrong asset name": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"][0].Assets = []Asset{{ID: 11, Name: "hello.wasm", Size: len(helloWasm)}}
		}, "no asset named plugin.wasm"},
		"asset too big": {func(s *memSource, f *FakeValidator) {
			s.releases["o/hello"][0].Assets = []Asset{{ID: 11, Name: "plugin.wasm", Size: MaxAssetBytes + 1}}
		}, "16"},
		"asset download fails": {func(s *memSource, f *FakeValidator) {
			delete(s.assets, 11)
		}, "no asset 11"},
		"not wasm runtime": {func(s *memSource, f *FakeValidator) {
			f.Infos[Sum(helloWasm)] = plugin.Info{Name: "hello", DisplayName: "Hello", Version: "1.1.0"}
		}, "runtime"},
		"missing readme": {func(s *memSource, f *FakeValidator) {
			delete(s.files, "o/hello@v1.1.0:README.md")
		}, "README.md"},
		"does not load": {func(s *memSource, f *FakeValidator) {
			f.Err = errors.New("wasm: boom")
		}, "boom"},
		"name mismatch": {func(s *memSource, f *FakeValidator) {
			f.Infos[Sum(helloWasm)] = plugin.Info{Name: "other", Version: "1.1.0", Runtime: "wasm"}
		}, "Name()"},
		"version mismatch": {func(s *memSource, f *FakeValidator) {
			f.Infos[Sum(helloWasm)] = plugin.Info{Name: "hello", Version: "1.0.9", Runtime: "wasm"}
		}, "Version()"},
	}
	for name, c := range cases {
		s, f := helloSource(), helloValidator()
		c.mutate(s, f)
		repo := "o/hello"
		if name == "bad repo string" {
			repo = "hello"
		}
		_, err := ValidateEntry(context.Background(), s, f, repo)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}

func TestLatestVersion(t *testing.T) {
	v, err := LatestVersion(context.Background(), helloSource(), "o/hello")
	if err != nil || v != "1.1.0" {
		t.Errorf("LatestVersion = %q, %v", v, err)
	}
	src := helloSource()
	src.releases["o/hello"] = nil
	if _, err := LatestVersion(context.Background(), src, "o/hello"); err == nil {
		t.Error("no releases must be an error")
	}
}
