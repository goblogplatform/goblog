package tools_test

import (
	"os"
	"testing"

	"goblog/auth"
	"goblog/blog"
	"goblog/tools"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Auth is a stand-in IAuth: the blog only needs it to construct.
type Auth struct{}

func (Auth) IsAdmin(*gin.Context) bool               { return false }
func (Auth) IsLoggedIn(*gin.Context) bool            { return false }
func (Auth) IsWizardMode(*gin.Context) bool          { return false }
func (Auth) EmailLoginEnabled() bool                 { return false }
func (Auth) CurrentUser(*gin.Context) *auth.BlogUser { return nil }

// openTestPostgres connects to the Postgres instance named by
// GOBLOG_TEST_POSTGRES_DSN (CI provides one) and returns an empty schema,
// or skips the test when none is configured.
func openTestPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GOBLOG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOBLOG_TEST_POSTGRES_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	// Start from nothing so the test proves Migrate can build the schema.
	if err := db.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public").Error; err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	return db
}

// TestMigrationPostgres proves the migrations and the queries that use raw
// SQL work on Postgres (issue #14): Migrate builds the schema from scratch,
// is idempotent, seeds defaults, and the blog's search is case-insensitive
// as it is on SQLite and MySQL.
func TestMigrationPostgres(t *testing.T) {
	db := openTestPostgres(t)

	for i := 0; i < 2; i++ {
		if err := tools.Migrate(db); err != nil {
			t.Fatalf("migration run %d failed: %v", i+1, err)
		}
	}

	var settings []blog.Setting
	db.Find(&settings)
	if len(settings) == 0 {
		t.Fatal("expected default settings to be seeded")
	}
	var pt blog.PostType
	if err := db.Where("slug = ?", "posts").First(&pt).Error; err != nil {
		t.Fatalf("expected default post type: %v", err)
	}

	b := blog.New(db, &Auth{}, "test")
	post := blog.Post{Title: "Postgres Support Landed", Content: "Some content about PostgreSQL", Slug: "postgres-support", PostTypeID: pt.ID}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	db.Create(&blog.Comment{PostID: post.ID, Name: "A", Content: "c"})
	db.Create(&blog.ExternalBacklink{PostID: post.ID, Referer: "https://example.com/x", HitCount: 1})
	db.Create(&auth.BlogUser{ID: 1, Login: "operator"})
	db.Create(&auth.AdminUser{BlogUserID: 1})

	for _, q := range []string{"postgres", "POSTGRES", "PostgreSQL"} {
		if got := b.SearchPosts(q); len(got) != 1 {
			t.Errorf("search %q: expected 1 result, got %d", q, len(got))
		}
	}
	if got := b.SearchPosts("100%"); len(got) != 0 {
		t.Errorf("LIKE wildcards in the query must be escaped, got %d results", len(got))
	}
	if got := b.GetExternalBacklinks(post.ID); len(got) != 1 {
		t.Errorf("expected 1 external backlink, got %d", len(got))
	}
	if got, total := b.GetComments(0, 10); len(got) != 1 || total != 1 {
		t.Errorf("expected 1 comment, got %d (total %d)", len(got), total)
	}
}

// TestMigrationPostgres_UserIDSequence covers issue #523: GitHub users were
// inserted with explicit ids, which does not advance the Postgres sequence.
// After Migrate the next DB-assigned id must be past the largest existing one.
func TestMigrationPostgres_UserIDSequence(t *testing.T) {
	db := openTestPostgres(t)
	if err := tools.Migrate(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if err := db.Exec(`INSERT INTO blog_users (id, login) VALUES (23049896, 'legacy')`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := tools.Migrate(db); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}

	fresh := auth.BlogUser{Provider: auth.ProviderEmail, ProviderID: "new@example.com", Login: "new@example.com"}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("create without explicit id: %v", err)
	}
	if fresh.ID != 23049897 {
		t.Fatalf("expected DB-assigned id 23049897, got %d", fresh.ID)
	}
	var provider string
	db.Raw("SELECT provider FROM blog_users WHERE id = 23049896").Scan(&provider)
	if provider != auth.ProviderGitHub {
		t.Fatalf("expected legacy row backfilled as github, got %q", provider)
	}
}
