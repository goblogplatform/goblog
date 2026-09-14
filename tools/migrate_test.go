package tools_test

import (
	"goblog/blog"
	"goblog/tools"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
)

func TestMigration(t *testing.T) {
	os.Remove("test.db")
	db, err := gorm.Open(sqlite.Open("test.db"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite db: 'test.db'")
	}
	t.Cleanup(func() { os.Remove("test.db") })
	err = tools.Migrate(db)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify default post type was seeded
	var ptCount int64
	db.Raw("SELECT count(*) FROM post_types").Scan(&ptCount)
	if ptCount != 1 {
		t.Fatalf("expected 1 post type, got %d", ptCount)
	}

	var ptSlug string
	db.Raw("SELECT slug FROM post_types LIMIT 1").Scan(&ptSlug)
	if ptSlug != "posts" {
		t.Fatalf("expected default post type slug 'posts', got '%s'", ptSlug)
	}

	// Verify Writing page has PostTypeID linked to the default post type
	var pagePostTypeID *uint
	db.Raw("SELECT post_type_id FROM pages WHERE slug = 'posts'").Scan(&pagePostTypeID)
	if pagePostTypeID == nil {
		t.Fatal("expected Writing page to have post_type_id set, got nil")
	}

	var ptID uint
	db.Raw("SELECT id FROM post_types WHERE slug = 'posts'").Scan(&ptID)
	if *pagePostTypeID != ptID {
		t.Fatalf("expected Writing page post_type_id = %d, got %d", ptID, *pagePostTypeID)
	}
}

// TestMigrationWithOldSchema reproduces the production issue where blog_users
// was created with id varchar(255) and a table-level PRIMARY KEY by an older
// GORM version. This caused "more than one primary key" when AutoMigrate tried
// to alter the column to integer PRIMARY KEY AUTOINCREMENT.
func TestMigrationWithOldSchema(t *testing.T) {
	os.Remove("test_old_schema.db")
	db, err := gorm.Open(sqlite.Open("test_old_schema.db"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { os.Remove("test_old_schema.db") })

	// Exact DDL from the production goblog.db
	if err := db.Exec(`CREATE TABLE "blog_users" ("id" varchar(255),"github_id" integer,
		"login" text,"avatar_url" text,"name" text,"email" text,"access_token" text,
		PRIMARY KEY ("id"))`).Error; err != nil {
		t.Fatalf("failed to create old schema: %v", err)
	}

	// Also create tags with the old schema (table-level PK)
	if err := db.Exec(`CREATE TABLE "tags" ("name" varchar(255), PRIMARY KEY ("name"))`).Error; err != nil {
		t.Fatalf("failed to create old tags schema: %v", err)
	}

	// Insert test data with a mix of valid ids and NULL ids (as seen in production)
	if err := db.Exec(`INSERT INTO blog_users (id, github_id, login, name) VALUES
		('23049896', 23049896, 'testuser', 'Test User'),
		(NULL, NULL, 'nulluser', 'Null User')`).Error; err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	if err := db.Exec(`INSERT INTO tags (name) VALUES ('golang'), ('sqlite')`).Error; err != nil {
		t.Fatalf("failed to insert tag data: %v", err)
	}

	err = tools.Migrate(db)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify user data survived
	var userCount int64
	db.Raw("SELECT count(*) FROM blog_users").Scan(&userCount)
	if userCount != 2 {
		t.Fatalf("expected 2 users, got %d", userCount)
	}

	// Verify the numeric id was preserved
	var login string
	db.Raw("SELECT login FROM blog_users WHERE id = 23049896").Scan(&login)
	if login != "testuser" {
		t.Fatalf("expected login 'testuser' for id 23049896, got '%s'", login)
	}

	var legacyProvider, legacyProviderID string
	db.Raw("SELECT provider, provider_id FROM blog_users WHERE id = 23049896").Row().Scan(&legacyProvider, &legacyProviderID)
	if legacyProvider != "github" || legacyProviderID != "23049896" {
		t.Fatalf("expected legacy user backfilled as github/23049896, got %q/%q", legacyProvider, legacyProviderID)
	}

	// Verify tag data survived
	var tagCount int64
	db.Raw("SELECT count(*) FROM tags").Scan(&tagCount)
	if tagCount != 2 {
		t.Fatalf("expected 2 tags, got %d", tagCount)
	}
}

// TestMigrationBackfillsUserProvider covers issue #523: rows created before
// BlogUser had a provider pair are GitHub users whose id was the GitHub id.
// Migrate must tag them as such and leave rows that already have a provider
// alone.
func TestMigrationBackfillsUserProvider(t *testing.T) {
	os.Remove("test_provider.db")
	db, err := gorm.Open(sqlite.Open("test_provider.db"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { os.Remove("test_provider.db") })

	if err := tools.Migrate(db); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}
	if err := db.Exec(`INSERT INTO blog_users (id, login, provider, provider_id) VALUES
		(23049896, 'legacy', NULL, NULL),
		(23049897, 'legacy-empty', '', ''),
		(5, 'someone@example.com', 'email', 'someone@example.com')`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := tools.Migrate(db); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}

	type row struct {
		Provider   string
		ProviderID string
	}
	want := map[int]row{
		23049896: {"github", "23049896"},
		23049897: {"github", "23049897"},
		5:        {"email", "someone@example.com"},
	}
	for id, w := range want {
		var got row
		db.Raw("SELECT provider, provider_id FROM blog_users WHERE id = ?", id).Scan(&got)
		if got != w {
			t.Errorf("id %d: want %+v, got %+v", id, w, got)
		}
	}
}

// TestMigrationFixesTagsWithoutPrimaryKey reproduces the production issue where
// the tags table was created without a PRIMARY KEY, allowing duplicate rows.
func TestMigrationFixesTagsWithoutPrimaryKey(t *testing.T) {
	os.Remove("test_tags_no_pk.db")
	db, err := gorm.Open(sqlite.Open("test_tags_no_pk.db"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() { os.Remove("test_tags_no_pk.db") })

	// Create tags table WITHOUT primary key (as seen on staging)
	if err := db.Exec(`CREATE TABLE "tags" ("name" varchar(255))`).Error; err != nil {
		t.Fatalf("failed to create legacy tags table: %v", err)
	}

	// Insert duplicates (as seen in production: 14 "android" rows, etc.)
	for i := 0; i < 5; i++ {
		db.Exec(`INSERT INTO tags (name) VALUES ('android')`)
	}
	for i := 0; i < 3; i++ {
		db.Exec(`INSERT INTO tags (name) VALUES ('golang')`)
	}
	db.Exec(`INSERT INTO tags (name) VALUES ('sqlite')`)

	// Verify duplicates exist before migration
	var beforeCount int64
	db.Raw("SELECT count(*) FROM tags").Scan(&beforeCount)
	if beforeCount != 9 {
		t.Fatalf("expected 9 tag rows before migration, got %d", beforeCount)
	}

	err = tools.Migrate(db)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify duplicates were removed
	var afterCount int64
	db.Raw("SELECT count(*) FROM tags").Scan(&afterCount)
	if afterCount != 3 {
		t.Fatalf("expected 3 unique tags after migration, got %d", afterCount)
	}

	// Verify PRIMARY KEY was added
	var createSQL string
	db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND tbl_name = 'tags'").Scan(&createSQL)
	if !strings.Contains(strings.ToUpper(createSQL), "PRIMARY KEY") {
		t.Fatalf("expected tags table to have PRIMARY KEY, got: %s", createSQL)
	}

	// Verify inserting a duplicate now fails
	result := db.Exec(`INSERT INTO tags (name) VALUES ('android')`)
	if result.Error == nil {
		t.Fatal("expected duplicate insert to fail after PRIMARY KEY added")
	}
}

// TestMigrationCleansUpSelfExternalBacklinks verifies that external backlink rows
// recorded before the self-referral fix (#539) are removed on migration, while
// genuine external referers are kept.
func TestMigrationCleansUpSelfExternalBacklinks(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	if err := tools.Migrate(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	db.Model(&blog.Setting{}).Where("key = ?", "site_url").Update("value", "https://www.myblog.com")

	referers := []string{
		"https://www.myblog.com/posts/2026/03/16/a-post",
		"https://myblog.com/posts/2026/03/16/a-post",
		"https://159.89.157.125/posts/2026/03/16/a-post",
		"http://localhost:7000/posts/2026/03/16/a-post",
		"https://www.google.com/",
		"https://example.com/some-page",
	}
	for _, r := range referers {
		db.Create(&blog.ExternalBacklink{PostID: 1, Referer: r, HitCount: 1})
	}

	if err := tools.Migrate(db); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}

	var remaining []blog.ExternalBacklink
	db.Order("referer asc").Find(&remaining)
	want := []string{"https://example.com/some-page", "https://www.google.com/"}
	if len(remaining) != len(want) {
		t.Fatalf("expected %d external backlinks to remain, got %d: %+v", len(want), len(remaining), remaining)
	}
	for i, r := range remaining {
		if r.Referer != want[i] {
			t.Errorf("expected remaining referer %q, got %q", want[i], r.Referer)
		}
	}
}
