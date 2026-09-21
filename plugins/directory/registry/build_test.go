package registry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRepo_Good(t *testing.T) {
	src := helloSource()
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	want := IndexEntry{
		Kind: "plugin",
		Name: "hello", DisplayName: "Hello", Description: "Says hi.", Version: "1.1.0", Author: "Jason Ernst",
		License: "Apache-2.0", SourceURL: "https://github.com/o/hello",
		DownloadURL: "https://github.com/o/hello/releases/download/v1.1.0/plugin.wasm", SHA256: Sum(helloWasm),
		MinGoblogVersion: "0.2.6", InstallType: "wasm", Runtime: "wasm", AllowedHosts: []string{"api.example.test"},
		ReleasedAt: "2026-09-15T00:00:00Z",
		DetailURL:  "https://example.test/plugins/hello.json", Stars: 7,
	}
	if !reflect.DeepEqual(d.IndexEntry, want) {
		t.Errorf("entry =\n%+v\nwant\n%+v", d.IndexEntry, want)
	}
	if d.ReadmeHTML != "<p># Hello</p>" || d.ChangelogHTML != "<p>## 1.1.0\n- second</p>" {
		t.Errorf("readme/changelog = %q %q", d.ReadmeHTML, d.ChangelogHTML)
	}
	if len(d.Releases) != 2 || d.Releases[0].Version != "1.1.0" || d.Releases[0].NotesHTML != "<p>Second</p>" ||
		d.Releases[1].Version != "1.0.0" || d.Releases[1].URL != "https://github.com/o/hello/releases/tag/v1.0.0" {
		t.Errorf("releases = %+v", d.Releases)
	}
}

func TestBuildRepo_RelativeDetailURLWithoutBase(t *testing.T) {
	d, err := BuildRepo(context.Background(), helloSource(), helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.DetailURL != "/plugins/hello.json" {
		t.Errorf("detail_url = %q", d.DetailURL)
	}
}

func TestBuildRepo_NoChangelogAndEmptyHostsSerialiseCleanly(t *testing.T) {
	src := helloSource()
	delete(src.files, "o/hello@v1.1.0:CHANGELOG.md")
	src.files["o/hello@v1.1.0:goblog-plugin.json"] = strings.Replace(goodManifest, `"allowed_hosts": ["api.example.test"],`, "", 1)
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.ChangelogHTML != "" || d.AllowedHosts == nil || len(d.AllowedHosts) != 0 {
		t.Errorf("doc = %+v", d)
	}
}

func TestBuildRepo_StarsAreBestEffort(t *testing.T) {
	src := helloSource()
	src.starsErr = errors.New("github down")
	d, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Stars != 0 {
		t.Errorf("stars should fall back to 0, got %d", d.Stars)
	}
}

func TestBuildRepo_RenderedReadmeOverLimitIsRefused(t *testing.T) {
	src := helloSource()
	src.files["o/hello@v1.1.0:README.md"] = strings.Repeat("a", MaxRenderedBytes+1)
	_, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err == nil || !strings.Contains(err.Error(), "README.md") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("want a README size-limit error, got %v", err)
	}
}

func TestBuildRepo_RenderedChangelogOverLimitIsRefused(t *testing.T) {
	src := helloSource()
	src.files["o/hello@v1.1.0:CHANGELOG.md"] = strings.Repeat("a", MaxRenderedBytes+1)
	_, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err == nil || !strings.Contains(err.Error(), "CHANGELOG.md") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("want a CHANGELOG size-limit error, got %v", err)
	}
}

// TestBuildRepo_ReleaseNotesOverLimitAreRefused: a release body renders to
// notes_html in the detail document, so it is capped like the README.
func TestBuildRepo_ReleaseNotesOverLimitAreRefused(t *testing.T) {
	src := helloSource()
	src.releases["o/hello"][3].Body = strings.Repeat("a", MaxRenderedBytes+1)
	_, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", "")
	if err == nil || !strings.Contains(err.Error(), "v1.0.0") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("want a release-notes size-limit error naming the release, got %v", err)
	}
}

// TestBuildRepo_FetchesReadmeOnce: README.md is read once — the validator
// keeps the bytes for the docs render rather than probing for the file and
// fetching it again (one GitHub API call per build, which matters at 60/h
// unauthenticated).
func TestBuildRepo_FetchesReadmeOnce(t *testing.T) {
	src := helloSource()
	if _, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", ""); err != nil {
		t.Fatal(err)
	}
	if n := src.fetched["o/hello@v1.1.0:README.md"]; n != 1 {
		t.Errorf("README.md fetched %d times, want 1", n)
	}
}

func TestBuildRepo_ValidationErrorsPropagate(t *testing.T) {
	src := helloSource()
	delete(src.files, "o/hello@v1.1.0:README.md")
	if _, err := BuildRepo(context.Background(), src, helloValidator(), "o/hello", ""); err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Errorf("want README error, got %v", err)
	}
}
