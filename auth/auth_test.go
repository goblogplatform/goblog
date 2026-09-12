package auth_test

import (
	"errors"
	"net/http/httptest"
	"testing"

	"goblog/auth"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	r := gin.Default()
	store := cookie.NewStore([]byte("test"))
	r.Use(sessions.Sessions("session", store))
	c.Request = httptest.NewRequest("GET", "/", nil)
	r.HandleContext(c)
	return c
}

// newAuth returns an Auth on a fresh in-memory DB. It also clears the
// admin_login / admin_github_id pin so tests are hermetic regardless of the
// developer's or CI's environment; tests that exercise the pin set it after
// calling newAuth.
func newAuth(t *testing.T) (*auth.Auth, *gorm.DB) {
	t.Helper()
	t.Setenv("admin_login", "")
	t.Setenv("admin_github_id", "")
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&auth.BlogUser{}, &auth.AdminUser{}, &auth.LoginCode{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a := auth.New(db, "test")
	return &a, db
}

func TestIsAdmin_NoAdminUser_ReturnsFalse(t *testing.T) {
	a, _ := newAuth(t)
	if a.IsAdmin(newCtx()) {
		t.Fatal("IsAdmin must be false on a fresh install with no admin_users row; otherwise anonymous traffic is treated as admin")
	}
}

func TestIsWizardMode_NoAdminUser_ReturnsTrue(t *testing.T) {
	a, _ := newAuth(t)
	if !a.IsWizardMode(newCtx()) {
		t.Fatal("IsWizardMode must be true when no admin_users row exists")
	}
}

func TestIsWizardMode_WithAdminUser_ReturnsFalse(t *testing.T) {
	a, db := newAuth(t)
	user := auth.BlogUser{ID: 1, Provider: auth.ProviderGitHub, ProviderID: "1", Login: "admin"}
	db.Create(&user)
	db.Create(&auth.AdminUser{BlogUserID: user.ID, BlogUser: user})
	if a.IsWizardMode(newCtx()) {
		t.Fatal("IsWizardMode must be false once an admin_users row exists")
	}
}

func TestEnsureAdmin_NoAdmin_CreatesRow(t *testing.T) {
	a, db := newAuth(t)
	user := auth.BlogUser{ID: 42, Provider: auth.ProviderGitHub, ProviderID: "42", Login: "operator"}
	db.Create(&user)

	if err := a.EnsureAdmin(&user); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}

	var count int64
	db.Model(&auth.AdminUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 admin_users row, got %d", count)
	}
	var got auth.AdminUser
	db.First(&got)
	if got.BlogUserID != user.ID {
		t.Fatalf("expected admin BlogUserID=%d, got %d", user.ID, got.BlogUserID)
	}
}

func TestEnsureAdmin_AdminExists_NoOp(t *testing.T) {
	a, db := newAuth(t)
	first := auth.BlogUser{ID: 1, Provider: auth.ProviderGitHub, ProviderID: "1", Login: "first"}
	second := auth.BlogUser{ID: 2, Provider: auth.ProviderGitHub, ProviderID: "2", Login: "second"}
	db.Create(&first)
	db.Create(&second)
	db.Create(&auth.AdminUser{BlogUserID: first.ID, BlogUser: first})

	if err := a.EnsureAdmin(&second); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}

	var count int64
	db.Model(&auth.AdminUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected EnsureAdmin to be a no-op when an admin exists, got %d admin rows", count)
	}
	var got auth.AdminUser
	db.First(&got)
	if got.BlogUserID != first.ID {
		t.Fatalf("expected first admin to remain (id=%d), got id=%d", first.ID, got.BlogUserID)
	}
}

// adminCount returns the number of admin_users rows.
func adminCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	db.Model(&auth.AdminUser{}).Count(&count)
	return count
}

// TestEnsureAdmin_PinnedIdentity covers issue #532: when admin_login and/or
// admin_github_id are configured, only the matching GitHub identity is
// promoted; when neither is set, first-to-login wins as before.
func TestEnsureAdmin_PinnedIdentity(t *testing.T) {
	cases := []struct {
		name        string
		adminLogin  string
		adminID     string
		user        auth.BlogUser
		wantPromote bool
	}{
		{"neither set: first login promoted", "", "", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "anyone"}, true},
		{"login matches", "operator", "", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "operator"}, true},
		{"login matches case-insensitively", "Operator", "", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "operator"}, true},
		{"login mismatch", "operator", "", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "intruder"}, false},
		{"id matches", "", "7", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "whatever"}, true},
		{"id mismatch", "", "7", auth.BlogUser{ID: 8, Provider: auth.ProviderGitHub, ProviderID: "8", Login: "operator"}, false},
		{"both set, login matches", "operator", "999", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "operator"}, true},
		{"both set, id matches", "someone-else", "7", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "operator"}, true},
		{"both set, neither matches", "operator", "7", auth.BlogUser{ID: 8, Provider: auth.ProviderGitHub, ProviderID: "8", Login: "intruder"}, false},
		{"malformed id never matches", "", "not-a-number", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "operator"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, db := newAuth(t)
			t.Setenv("admin_login", tc.adminLogin)
			t.Setenv("admin_github_id", tc.adminID)
			db.Create(&tc.user)

			err := a.EnsureAdmin(&tc.user)
			if tc.wantPromote {
				if err != nil {
					t.Fatalf("expected promotion, got error: %v", err)
				}
				if got := adminCount(t, db); got != 1 {
					t.Fatalf("expected 1 admin row, got %d", got)
				}
				return
			}
			if !errors.Is(err, auth.ErrNotConfiguredAdmin) {
				t.Fatalf("expected ErrNotConfiguredAdmin, got %v", err)
			}
			if got := adminCount(t, db); got != 0 {
				t.Fatalf("expected no admin row after refused promotion, got %d", got)
			}
		})
	}
}

func TestEnsureAdmin_PinnedIdentity_AdminExists_NoOp(t *testing.T) {
	a, db := newAuth(t)
	t.Setenv("admin_login", "operator")
	first := auth.BlogUser{ID: 1, Provider: auth.ProviderGitHub, ProviderID: "1", Login: "operator"}
	second := auth.BlogUser{ID: 2, Provider: auth.ProviderGitHub, ProviderID: "2", Login: "operator"}
	db.Create(&first)
	db.Create(&second)
	db.Create(&auth.AdminUser{BlogUserID: first.ID, BlogUser: first})

	if err := a.EnsureAdmin(&second); err != nil {
		t.Fatalf("expected no-op when an admin exists, got %v", err)
	}
	if got := adminCount(t, db); got != 1 {
		t.Fatalf("expected admin count to stay 1, got %d", got)
	}
}
