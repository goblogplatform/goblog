package wizard

import (
	"strings"
	"testing"

	"goblog/auth"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newWizard(t *testing.T) (*Wizard, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&auth.BlogUser{}, &auth.AdminUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	w := New(db, "test")
	return &w, db
}

func adminCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	db.Model(&auth.AdminUser{}).Count(&n)
	return n
}

// TestCreateAdminUser_PromotesFirstUser covers the wizard's original behaviour:
// with no pin configured, the user completing the wizard becomes admin.
func TestCreateAdminUser_PromotesFirstUser(t *testing.T) {
	t.Setenv("admin_login", "")
	t.Setenv("admin_github_id", "")
	w, db := newWizard(t)

	user := &auth.BlogUser{ID: 42, Provider: auth.ProviderGitHub, ProviderID: "42", Login: "operator"}
	if err := w.createAdminUser(user); err != nil {
		t.Fatalf("createAdminUser: %v", err)
	}
	if got := adminCount(t, db); got != 1 {
		t.Fatalf("expected 1 admin row, got %d", got)
	}
	var blogUsers int64
	db.Model(&auth.BlogUser{}).Count(&blogUsers)
	if blogUsers != 1 {
		t.Fatalf("expected the blog user to be stored, got %d rows", blogUsers)
	}
}

// TestCreateAdminUser_RefusesUnconfiguredIdentity covers issue #532: the
// wizard honours the same admin pin as the login path and reports it visibly.
func TestCreateAdminUser_RefusesUnconfiguredIdentity(t *testing.T) {
	t.Setenv("admin_login", "operator")
	t.Setenv("admin_github_id", "")
	w, db := newWizard(t)

	err := w.createAdminUser(&auth.BlogUser{ID: 43, Provider: auth.ProviderGitHub, ProviderID: "43", Login: "intruder"})
	if err == nil {
		t.Fatal("expected an error for a user that is not the configured admin")
	}
	if !strings.Contains(err.Error(), "intruder") || !strings.Contains(err.Error(), "admin_login") {
		t.Fatalf("expected error to name the user and the .env setting, got %q", err.Error())
	}
	if got := adminCount(t, db); got != 0 {
		t.Fatalf("expected no admin row, got %d", got)
	}
}

// TestCreateAdminUser_ReturningUser_DoesNotDuplicate: the wizard can be re-run
// (or the same person can have logged in before); it must find the existing
// row instead of failing on a duplicate insert.
func TestCreateAdminUser_ReturningUser_DoesNotDuplicate(t *testing.T) {
	t.Setenv("admin_login", "")
	t.Setenv("admin_github_id", "")
	w, db := newWizard(t)

	existing := auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "42", Login: "operator"}
	db.Create(&existing)

	if err := w.createAdminUser(&auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "42", Login: "operator"}); err != nil {
		t.Fatalf("createAdminUser: %v", err)
	}
	var blogUsers int64
	db.Model(&auth.BlogUser{}).Count(&blogUsers)
	if blogUsers != 1 {
		t.Fatalf("expected 1 blog user, got %d", blogUsers)
	}
	var admin auth.AdminUser
	db.First(&admin)
	if admin.BlogUserID != existing.ID {
		t.Fatalf("expected admin to reference existing user %d, got %d", existing.ID, admin.BlogUserID)
	}
}

func TestIsDbNil_DoesNotInsertRows(t *testing.T) {
	w, db := newWizard(t)
	if w.IsDbNil() {
		t.Fatal("expected IsDbNil false with a db")
	}
	var n int64
	db.Model(&auth.BlogUser{}).Count(&n)
	if n != 0 {
		t.Fatalf("IsDbNil must not insert probe rows, found %d", n)
	}
}
