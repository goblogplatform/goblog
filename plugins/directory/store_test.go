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
		IndexEntry: registry.IndexEntry{Kind: KindPlugin, Name: name, DisplayName: name, Description: "d", Version: version, Author: "a", License: "MIT",
			SourceURL: "https://github.com/o/" + name, DownloadURL: "https://github.com/o/" + name + "/releases/download/v" + version + "/plugin.wasm",
			SHA256: "00", MinGoblogVersion: "0.3.0", InstallType: "wasm", Runtime: "wasm", AllowedHosts: []string{}, ReleasedAt: "2026-09-15T00:00:00Z",
			DetailURL: "/plugins/" + name + ".json", Stars: stars},
		ReadmeHTML: "<p>readme</p>", Releases: []registry.ReleaseDoc{},
	}
}

// seed inserts a repo with a build of kind in the given status and returns it.
func seed(t *testing.T, db *gorm.DB, kind, repo, status string, d registry.DetailDoc) Repo {
	t.Helper()
	r := Repo{Repo: repo, Kind: kind, Status: status, SubmittedAt: time.Now()}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	b := Build{RepoID: r.ID, Kind: kind}
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
	seed(t, db, KindPlugin, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	if err := db.Create(&Repo{Repo: "o/hello", Status: StatusPending, SubmittedAt: time.Now()}).Error; err == nil {
		t.Error("repo must be unique")
	}
	other := Repo{Repo: "o/other", Status: StatusPending, SubmittedAt: time.Now()}
	db.Create(&other)
	b := Build{RepoID: other.ID, Kind: KindPlugin}
	b.SetDoc(doc("hello", "2.0.0", 0))
	if err := db.Create(&b).Error; err == nil {
		t.Error("build name must be unique")
	}
}

func TestMigrate_BackfillsKindAndDropsNameIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-kind schema: raw SQL rather than
	// db.Table(...).AutoMigrate(&oldBuild{}), because gorm derives an index
	// name from the struct type (idx_old_builds_name), not the table it is
	// created on — the struct approach would leave the real
	// idx_directory_builds_name untested by the drop below.
	db.Exec("CREATE TABLE directory_repos (id INTEGER PRIMARY KEY AUTOINCREMENT, repo TEXT, status TEXT, submitted_at DATETIME, decided_at DATETIME, reject_reason TEXT, submitter_ip TEXT)")
	db.Exec("CREATE UNIQUE INDEX idx_directory_repos_repo ON directory_repos(repo)")
	db.Exec("CREATE TABLE directory_builds (id INTEGER PRIMARY KEY AUTOINCREMENT, repo_id INTEGER, name TEXT, version TEXT, stars INTEGER, doc TEXT, built_at DATETIME, last_attempt_at DATETIME, last_error TEXT)")
	db.Exec("CREATE UNIQUE INDEX idx_directory_builds_repo_id ON directory_builds(repo_id)")
	db.Exec("CREATE UNIQUE INDEX idx_directory_builds_name ON directory_builds(name)")
	db.Exec("INSERT INTO directory_repos (repo, status, submitted_at) VALUES (?, ?, ?)", "o/hello", StatusApproved, time.Now())
	db.Exec("INSERT INTO directory_builds (repo_id, name, doc) VALUES (?, ?, ?)", 1, "hello", "{}")

	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var r Repo
	db.First(&r)
	var b Build
	db.First(&b)
	if r.Kind != KindPlugin || b.Kind != KindPlugin {
		t.Errorf("existing rows must become plugins: %q %q", r.Kind, b.Kind)
	}
	if db.Migrator().HasIndex(&Build{}, "idx_directory_builds_name") {
		t.Error("the old name-only unique index must be dropped")
	}
	// A theme may now share the name.
	theme := Repo{Repo: "o/hello-theme", Kind: KindTheme, Status: StatusApproved, SubmittedAt: time.Now()}
	db.Create(&theme)
	tb := Build{RepoID: theme.ID, Kind: KindTheme}
	tb.SetDoc(doc("hello", "1.0.0", 0))
	if err := db.Create(&tb).Error; err != nil {
		t.Errorf("a theme and a plugin may share a name: %v", err)
	}
	// But two of the same kind may not.
	dup := Build{RepoID: 99, Kind: KindPlugin}
	dup.SetDoc(doc("hello", "1.0.0", 0))
	if err := db.Create(&dup).Error; err == nil {
		t.Error("(kind, name) must be unique")
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
	seed(t, db, KindPlugin, "o/zeta", StatusApproved, doc("zeta", "0.1.0", 3))
	seed(t, db, KindPlugin, "o/hello", StatusApproved, doc("hello", "1.0.0", 1))
	seed(t, db, KindPlugin, "o/pending", StatusPending, doc("pending", "1.0.0", 99))
	seed(t, db, KindPlugin, "o/rejected", StatusRejected, doc("rejected", "1.0.0", 99))
	seed(t, db, KindTheme, "o/ocean", StatusApproved, doc("ocean", "1.0.0", 3))

	docs, err := approvedDocs(db, KindPlugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Name != "hello" || docs[1].Name != "zeta" {
		t.Fatalf("approved docs should be the approved plugins sorted by name: %+v", docs)
	}
	themeDocs, err := approvedDocs(db, KindTheme)
	if err != nil {
		t.Fatal(err)
	}
	if len(themeDocs) != 1 || themeDocs[0].Name != "ocean" {
		t.Fatalf("approved docs by kind theme should return only the theme: %+v", themeDocs)
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
