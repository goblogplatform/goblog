package directory

import (
	"encoding/json"
	"testing"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func doc(name, version string, stars int) registry.DetailDoc {
	return registry.DetailDoc{
		IndexEntry: registry.IndexEntry{Name: name, DisplayName: name, Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: "https://github.com/o/" + name + "/releases/download/v" + version + "/plugin.wasm",
			SHA256: "00", MinGoblogVersion: "0.3.0", InstallType: "wasm", Runtime: "wasm", AllowedHosts: []string{}, ReleasedAt: "2026-09-15T00:00:00Z",
			DetailURL: "/plugins/" + name + ".json", Stars: stars},
		ReadmeHTML: "<p>readme</p>", Releases: []registry.ReleaseDoc{},
	}
}

// seed inserts a repo with a build in the given status and returns it.
func seed(t *testing.T, db *gorm.DB, repo, status string, d registry.DetailDoc) Repo {
	t.Helper()
	r := Repo{Repo: repo, Status: status, SubmittedAt: time.Now()}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	b := Build{RepoID: r.ID}
	if err := b.SetDoc(d); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMigrate_IsIdempotentAndEnforcesUniqueness(t *testing.T) {
	db := testDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seed(t, db, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	if err := db.Create(&Repo{Repo: "o/hello", Status: StatusPending, SubmittedAt: time.Now()}).Error; err == nil {
		t.Error("repo must be unique")
	}
	other := Repo{Repo: "o/other", Status: StatusPending, SubmittedAt: time.Now()}
	db.Create(&other)
	b := Build{RepoID: other.ID}
	b.SetDoc(doc("hello", "2.0.0", 0))
	if err := db.Create(&b).Error; err == nil {
		t.Error("build name must be unique")
	}
}

func TestBuild_DocRoundTrip(t *testing.T) {
	var b Build
	want := doc("hello", "1.2.3", 9)
	if err := b.SetDoc(want); err != nil {
		t.Fatal(err)
	}
	if b.Name != "hello" || b.Version != "1.2.3" || b.Stars != 9 || b.BuiltAt.IsZero() || b.LastError != "" {
		t.Errorf("build = %+v", b)
	}
	got, err := b.Detail()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "hello" || got.ReadmeHTML != "<p>readme</p>" || got.AllowedHosts == nil {
		t.Errorf("detail = %+v", got)
	}
}

func TestApprovedDocsAndEncodeIndex(t *testing.T) {
	db := testDB(t)
	seed(t, db, "o/zeta", StatusApproved, doc("zeta", "0.1.0", 3))
	seed(t, db, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	seed(t, db, "o/pending", StatusPending, doc("pending", "1.0.0", 99))
	seed(t, db, "o/rejected", StatusRejected, doc("rejected", "1.0.0", 99))

	docs, err := approvedDocs(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Name != "hello" || docs[1].Name != "zeta" {
		t.Fatalf("approved docs should be the approved ones sorted by name: %+v", docs)
	}
	raw, entries := encodeIndex(docs)
	var decoded []registry.IndexEntry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0].Name != "hello" || decoded[0].AllowedHosts == nil || len(entries) != 2 {
		t.Errorf("index = %s", raw)
	}
	if raw, _ := encodeIndex(nil); string(raw) != "[]\n" {
		t.Errorf("empty index must be [], got %q", raw)
	}
}
