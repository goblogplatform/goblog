# Email OTP Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let visitors log in with an emailed 6-digit code alongside GitHub, with `BlogUser` made provider-agnostic so both identities live in one table.

**Architecture:** `BlogUser` gains a `(Provider, ProviderID)` pair and stops using the GitHub id as its primary key; a dialect-neutral backfill tags existing rows as GitHub. A new `mail` package sends over SMTP configured in `.env`. Two handlers in `auth/otp.go` issue and verify hashed, expiring codes stored in a `login_codes` table, then create/refresh the email user and set the same session token `IsLoggedIn` already checks.

**Tech Stack:** Go 1.26, gin, gorm (sqlite/mysql/postgres), stdlib `net/smtp` + `crypto/tls`, jQuery in the theme templates.

**Spec:** `docs/superpowers/specs/2026-09-12-otp-email-login-design.md`

## Global Constraints

- No new Go module dependencies; SMTP via stdlib only.
- Admin promotion stays GitHub-only: `EnsureAdmin` is never called from the email path.
- `.env` keys are lowercase like the existing ones: `smtp_host`, `smtp_port` (default `587`), `smtp_user`, `smtp_password`, `smtp_from`. Email login is enabled iff `smtp_host` and `smtp_from` are both non-empty.
- Code: 6 digits, 10-minute lifetime, max 5 wrong attempts, 60 s minimum between sends per address, one outstanding code per address.
- OTP endpoints return `404 {"error":"email login not configured"}` when no mailer is set.
- `POST /api/login/email` returns the same `200 {"status":"sent"}` whether or not the address is known.
- `AccessToken` is never serialised to JSON (`json:"-"`).
- Tests run with `go test goblog/...`; CI also runs postgres tests when `GOBLOG_TEST_POSTGRES_DSN` is set (see `tools/migrate_postgres_test.go`).
- Commit messages end with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- Work on branch `feat/523-otp-auth`. Run `git branch --show-current` before every commit.

### Deviations from the spec (decided while planning)

1. The upsert is `Auth.UpsertUser(*BlogUser)`, generic over provider, instead of a GitHub-only `upsertGitHubUser`. The email path needs the same find-or-create, so one function serves both.
2. `wizard.IsDbNil` currently inserts a blank `BlogUser` on every call. With a unique index on `(provider, provider_id)` a second blank insert would violate it, so `IsDbNil` becomes a plain nil check. The spec called this out as "not fixed here"; the index forces the fix.
3. `auth.EmailLoginEnabled()` delegates to `mail.NewSMTPSenderFromEnv()` rather than re-reading the env, so there's one definition of "configured".

---

## File map

| File | Responsibility |
|------|----------------|
| `auth/user.go` | `BlogUser` (provider pair), `LoginCode`, provider constants |
| `auth/auth.go` | GitHub login: `githubUser`, `RequestUser`, `UpsertUser`, `isConfiguredAdmin`, `Mailer` field, `EmailLoginEnabled` |
| `auth/otp.go` | `SendLoginCodeHandler`, `VerifyLoginCodeHandler`, code helpers |
| `auth/auth_test.go` | existing tests updated for the provider pair; `UpsertUser` tests |
| `auth/otp_test.go` | handler tests with a fake mailer |
| `mail/mail.go` | `Sender`, `SMTPSender`, `NewSMTPSenderFromEnv`, `Message` |
| `mail/mail_test.go` | message construction, env parsing |
| `tools/migrate.go` | `LoginCode` in AutoMigrate, `backfillUserProvider` |
| `tools/migrate_test.go`, `tools/migrate_postgres_test.go` | backfill and postgres sequence tests |
| `wizard/wizard.go`, `wizard/wizard_test.go` | `createAdminUser` via `UpsertUser`; `IsDbNil` fix |
| `blog/blog.go` | `Login` passes `email_login_enabled` |
| `goblog.go` | load `.env` into the environment, construct mailer, register routes |
| `themes/{default,forest,minimal}/templates/login.html` | email form |
| `template.env`, `README.md` | configuration docs |

---

### Task 1: Provider pair on `BlogUser`, `LoginCode` table, backfill migration

**Files:**
- Modify: `auth/user.go`
- Modify: `tools/migrate.go:370` (AutoMigrate list) and add `backfillUserProvider`
- Modify: `tools/migrate_test.go` (`TestMigrationWithOldSchema` + new test)
- Modify: `tools/migrate_postgres_test.go` (new test)
- Modify: `auth/auth_test.go`, `wizard/wizard_test.go` (only enough to compile and stay green: add `Provider`/`ProviderID` to `BlogUser` literals)

**Interfaces:**
- Produces: `auth.ProviderGitHub = "github"`, `auth.ProviderEmail = "email"`; `auth.BlogUser{ID, Provider, ProviderID, Login, AvatarURL, Name, Email, AccessToken}`; `auth.LoginCode{Email, CodeHash, ExpiresAt, Attempts, CreatedAt}`.

- [ ] **Step 1: Write the failing sqlite backfill test**

Append to `tools/migrate_test.go`:

```go
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
```

Also extend `TestMigrationWithOldSchema`: right after the existing "Verify user data survived" assertions, add:

```go
	var legacyProvider, legacyProviderID string
	db.Raw("SELECT provider, provider_id FROM blog_users WHERE id = 23049896").Row().Scan(&legacyProvider, &legacyProviderID)
	if legacyProvider != "github" || legacyProviderID != "23049896" {
		t.Fatalf("expected legacy user backfilled as github/23049896, got %q/%q", legacyProvider, legacyProviderID)
	}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test goblog/tools -run 'TestMigrationBackfillsUserProvider|TestMigrationWithOldSchema' -v`
Expected: FAIL — `no such column: provider`.

- [ ] **Step 3: Update `auth/user.go`**

Replace the whole file:

```go
package auth

import "time"

// Values for BlogUser.Provider.
const (
	ProviderGitHub = "github"
	ProviderEmail  = "email"
)

// BlogUser is a user of the blog from any login provider. The (Provider,
// ProviderID) pair identifies the user in the external system: the GitHub
// numeric id as a string, or the normalised email address. ID is an internal
// key assigned by the database. The only role that matters is admin (see
// AdminUser); otherwise users exist for comments.
type BlogUser struct {
	ID         int    `gorm:"primaryKey" json:"id"`
	Provider   string `gorm:"uniqueIndex:idx_blog_users_provider;size:32" json:"provider"`
	ProviderID string `gorm:"uniqueIndex:idx_blog_users_provider;size:255" json:"provider_id"`
	Login      string `json:"login"` // github handle, or the email address for email users
	AvatarURL  string `json:"avatar_url"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	// AccessToken is the session credential: the GitHub OAuth token for
	// GitHub users, a random token for email users. Never sent to clients.
	AccessToken string `json:"-"`
}

type AdminUser struct {
	BlogUserID int
	BlogUser   BlogUser
}

// LoginCode is an outstanding one-time email login code. One row per
// address; requesting a new code replaces it. The code itself is never
// stored, only its hex-encoded SHA-256.
type LoginCode struct {
	Email     string `gorm:"primaryKey;size:254"`
	CodeHash  string `gorm:"size:64"`
	ExpiresAt time.Time
	Attempts  int
	CreatedAt time.Time
}
```

- [ ] **Step 4: Add the backfill to `tools/migrate.go`**

Add `&auth.LoginCode{}` to the AutoMigrate call (after `&auth.AdminUser{}`), then directly after the AutoMigrate error check insert:

```go
	if err := backfillUserProvider(db); err != nil {
		log.Println("Error backfilling blog_users provider: " + err.Error())
		return err
	}
```

And add the function (near `fixBlogUsersTable`):

```go
// backfillUserProvider tags rows created before BlogUser had a provider pair
// (issue #523). Until then every user came from GitHub and the row id was
// the GitHub id, so provider_id is the id rendered as a string. Idempotent:
// rows that already have a provider are untouched.
//
// Postgres does not advance a sequence when rows are inserted with explicit
// ids, which is how every GitHub user was created until now, so the sequence
// is moved past MAX(id) or the first DB-assigned id would collide.
func backfillUserProvider(db *gorm.DB) error {
	cast := "CAST(id AS TEXT)"
	if db.Dialector.Name() == "mysql" {
		cast = "CAST(id AS CHAR)"
	}
	err := db.Exec("UPDATE blog_users SET provider = ?, provider_id = "+cast+
		" WHERE provider = '' OR provider IS NULL", auth.ProviderGitHub).Error
	if err != nil {
		return err
	}
	if db.Dialector.Name() == "postgres" {
		return db.Exec("SELECT setval(pg_get_serial_sequence('blog_users', 'id'), " +
			"COALESCE((SELECT MAX(id) FROM blog_users), 0) + 1, false)").Error
	}
	return nil
}
```

- [ ] **Step 5: Add the postgres sequence test**

Append to `tools/migrate_postgres_test.go`:

```go
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
```

(`goblog/auth` is already imported in that file.)

- [ ] **Step 6: Give existing test fixtures a provider pair**

In `auth/auth_test.go` every `auth.BlogUser{ID: N, Login: "x"}` literal becomes `auth.BlogUser{ID: N, Provider: auth.ProviderGitHub, ProviderID: "N", Login: "x"}` (with the same N as a string). There are 12 such literals (`TestIsWizardMode_WithAdminUser`, `TestEnsureAdmin_NoAdmin_CreatesRow`, `TestEnsureAdmin_AdminExists_NoOp` ×2, the ten cases in `TestEnsureAdmin_PinnedIdentity`, `TestEnsureAdmin_PinnedIdentity_AdminExists_NoOp` ×2). Without a provider pair the new unique index makes the second blank row in a test collide.

In `wizard/wizard_test.go` the two literals become `&auth.BlogUser{ID: 42, Provider: auth.ProviderGitHub, ProviderID: "42", Login: "operator"}` and `&auth.BlogUser{ID: 43, Provider: auth.ProviderGitHub, ProviderID: "43", Login: "intruder"}`.

In `auth/auth_test.go` `newAuth`, add `&auth.LoginCode{}` to the AutoMigrate list.

- [ ] **Step 7: Run the whole suite**

Run: `go build ./... && go test goblog/...`
Expected: all PASS (postgres test skips without the DSN). If you have docker: `docker run --rm -d -e POSTGRES_PASSWORD=pw -p 5433:5432 postgres:16-alpine` then `GOBLOG_TEST_POSTGRES_DSN='host=localhost port=5433 user=postgres password=pw dbname=postgres sslmode=disable' go test goblog/tools -run Postgres -v` — otherwise CI covers it.

- [ ] **Step 8: Commit**

```bash
git add auth/user.go tools/migrate.go tools/migrate_test.go tools/migrate_postgres_test.go auth/auth_test.go wizard/wizard_test.go
git commit -m "Give BlogUser a provider pair and backfill existing GitHub users (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: GitHub login via `UpsertUser`; pin checks against `ProviderID`

**Files:**
- Modify: `auth/auth.go` (`RequestUser`, `LoginPostHandler`, `isConfiguredAdmin`, new `githubUser`, `UpsertUser`)
- Modify: `wizard/wizard.go` (`IsDbNil`, `createAdminUser`)
- Test: `auth/auth_test.go`, `wizard/wizard_test.go`

**Interfaces:**
- Consumes: `BlogUser`, `ProviderGitHub` from Task 1.
- Produces: `func (a *Auth) UpsertUser(user *BlogUser) (*BlogUser, error)` — matches on `(Provider, ProviderID)`; creates on miss with a DB-assigned `ID`; on hit refreshes `Login`, `AvatarURL`, `Name`, `Email`, `AccessToken` and returns the stored row.

- [ ] **Step 1: Write the failing tests**

Append to `auth/auth_test.go`:

```go
func TestUpsertUser_NewUser_GetsDBAssignedID(t *testing.T) {
	a, db := newAuth(t)
	stored, err := a.UpsertUser(&auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "23049896", Login: "octocat", AccessToken: "tok1"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if stored.ID == 0 {
		t.Fatal("expected the database to assign an id")
	}
	if stored.ID == 23049896 {
		t.Fatal("the GitHub id must not be used as the internal id")
	}
	var count int64
	db.Model(&auth.BlogUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestUpsertUser_ReturningUser_MatchedByProviderID(t *testing.T) {
	a, db := newAuth(t)
	first, err := a.UpsertUser(&auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "23049896", Login: "octocat", Name: "Old", AccessToken: "tok1"})
	if err != nil {
		t.Fatalf("first UpsertUser: %v", err)
	}
	second, err := a.UpsertUser(&auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "23049896", Login: "octocat2", Name: "New", AccessToken: "tok2"})
	if err != nil {
		t.Fatalf("second UpsertUser: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same internal id, got %d then %d", first.ID, second.ID)
	}
	var count int64
	db.Model(&auth.BlogUser{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
	var got auth.BlogUser
	db.First(&got, first.ID)
	if got.Login != "octocat2" || got.Name != "New" || got.AccessToken != "tok2" {
		t.Fatalf("expected profile and token refreshed, got %+v", got)
	}
}

func TestUpsertUser_SameProviderIDDifferentProvider_IsDifferentUser(t *testing.T) {
	a, _ := newAuth(t)
	gh, _ := a.UpsertUser(&auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "7", Login: "seven"})
	em, err := a.UpsertUser(&auth.BlogUser{Provider: auth.ProviderEmail, ProviderID: "7", Login: "7"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if gh.ID == em.ID {
		t.Fatal("expected different providers with the same provider id to be different users")
	}
}

func TestBlogUser_JSONOmitsAccessToken(t *testing.T) {
	b, err := json.Marshal(auth.BlogUser{Login: "x", AccessToken: "supersecret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "supersecret") || strings.Contains(string(b), "access_token") {
		t.Fatalf("access token must not be serialised: %s", b)
	}
}
```

Add `"encoding/json"` and `"strings"` to the imports.

In `TestEnsureAdmin_PinnedIdentity` add two cases to the table:

```go
		{"id with leading zeros still matches", "", "007", auth.BlogUser{ID: 7, Provider: auth.ProviderGitHub, ProviderID: "7", Login: "whatever"}, true},
		{"email user is never the configured admin", "", "", auth.BlogUser{ID: 9, Provider: auth.ProviderEmail, ProviderID: "op@example.com", Login: "op@example.com"}, false},
```

And in `TestEnsureAdmin_PinnedIdentity`, the `"id matches"` / `"id mismatch"` / `"both set, id matches"` / `"both set, neither matches"` cases keep their `ID` values but the match is now on `ProviderID` — make sure `"id mismatch"` has `ProviderID: "8"` and `"both set, neither matches"` has `ProviderID: "8"` (they should already after Task 1 step 6).

In `wizard/wizard_test.go` append:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test goblog/auth goblog/wizard`
Expected: compile error `a.UpsertUser undefined`; after stubbing, `TestEnsureAdmin_PinnedIdentity/id_with_leading_zeros` and `TestIsDbNil_DoesNotInsertRows` fail.

- [ ] **Step 3: Implement in `auth/auth.go`**

Replace `RequestUser` so it unmarshals into a private struct and maps:

```go
// githubUser is the subset of GitHub's /user response we keep.
type githubUser struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Name      string `json:"name"`
	Email     string `json:"email"`
}

// RequestUser fetches the GitHub profile for accessToken and returns it as an
// unstored BlogUser (ID is zero; the database assigns it on UpsertUser).
func (a *Auth) RequestUser(accessToken string) (*BlogUser, error) {
	gh := &githubUser{}
	//get the user info from Github
	req, err := http.NewRequest("GET", "https://api.github.com/user", strings.NewReader(""))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+accessToken)
	fmt.Println("REQ" + a.formatRequest(req))

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	bodyString := string(bodyBytes)
	fmt.Println("github response:\n", bodyString) //todo: remove - just for debugging

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(bodyString)
	}

	json.Unmarshal(bodyBytes, gh)
	user := &BlogUser{
		Provider:    ProviderGitHub,
		ProviderID:  strconv.Itoa(gh.ID),
		Login:       gh.Login,
		AvatarURL:   gh.AvatarURL,
		Name:        gh.Name,
		Email:       gh.Email,
		AccessToken: accessToken,
	}

	fmt.Println("Parsed user: ", user.Login, user.ProviderID)

	return user, nil
}

// UpsertUser stores user, matching an existing row by (Provider, ProviderID).
// On a miss the row is created and the database assigns ID. On a hit the
// profile fields and AccessToken are refreshed. Either way the stored row is
// returned.
func (a *Auth) UpsertUser(user *BlogUser) (*BlogUser, error) {
	var existing BlogUser
	err := (*a.db).Where("provider = ? AND provider_id = ?", user.Provider, user.ProviderID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user.ID = 0
		if err := (*a.db).Create(user).Error; err != nil {
			return nil, err
		}
		return user, nil
	}
	if err != nil {
		return nil, err
	}
	existing.Login = user.Login
	existing.AvatarURL = user.AvatarURL
	existing.Name = user.Name
	existing.Email = user.Email
	existing.AccessToken = user.AccessToken
	// Save writes every column, so a field GitHub now hides (e.g. a private
	// email) is cleared rather than left stale.
	if err := (*a.db).Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}
```

In `LoginPostHandler` replace the block from `//check if user exists` through `existingUser = *user\n\t}` with:

```go
	stored, err := a.UpsertUser(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, "Error storing user: "+err.Error())
		return
	}
```

Then change `a.EnsureAdmin(user)` to `a.EnsureAdmin(stored)`, the log line's `user.Login, user.ID` to `stored.Login, stored.ProviderID` (with `%s` instead of `%d`), and the final `c.JSON(http.StatusOK, existingUser)` to `c.JSON(http.StatusOK, stored)`.

Replace `isConfiguredAdmin`:

```go
// isConfiguredAdmin reports whether user matches the admin identity pinned in
// .env. Only GitHub users can be admin. If neither admin_login nor
// admin_github_id is set, any GitHub user matches (first-to-login wins). If
// either is set, the user must match at least one: admin_login
// case-insensitively against the GitHub login, admin_github_id numerically
// against the GitHub id (ProviderID). A malformed admin_github_id is treated
// as set but never matching, so a typo cannot silently reopen the gate.
func isConfiguredAdmin(user *BlogUser) bool {
	if user.Provider != ProviderGitHub {
		return false
	}
	login := strings.TrimSpace(os.Getenv("admin_login"))
	idStr := strings.TrimSpace(os.Getenv("admin_github_id"))
	if login == "" && idStr == "" {
		return true
	}
	if login != "" && strings.EqualFold(login, user.Login) {
		return true
	}
	if idStr != "" {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			log.Printf("admin_github_id %q is not a number; no user can match it", idStr)
			return false
		}
		if githubID, err := strconv.Atoi(user.ProviderID); err == nil && githubID == id {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Update `wizard/wizard.go`**

```go
func (w *Wizard) IsDbNil() bool {
	return (*w.db) == nil
}
```

and

```go
// createAdminUser stores the GitHub user and promotes them to admin through
// the same gate as the login path, so an admin_login / admin_github_id pin in
// .env is honoured by the wizard too. A returning user is matched rather than
// duplicated.
func (w *Wizard) createAdminUser(user *auth.BlogUser) error {
	_auth := auth.New(*w.db, w.Version)
	stored, err := _auth.UpsertUser(user)
	if err != nil {
		return errors.New("Error creating user: " + err.Error())
	}
	err = _auth.EnsureAdmin(stored)
	if errors.Is(err, auth.ErrNotConfiguredAdmin) {
		return fmt.Errorf("GitHub user %q (id %s) is not the configured admin; check admin_login / admin_github_id in .env", stored.Login, stored.ProviderID)
	}
	if err != nil {
		return errors.New("Error creating admin user: " + err.Error())
	}
	return nil
}
```

- [ ] **Step 5: Run tests**

Run: `go build ./... && go test goblog/...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add auth/auth.go auth/auth_test.go wizard/wizard.go wizard/wizard_test.go
git commit -m "Match GitHub users by provider id and stop echoing the access token (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `mail` package

**Files:**
- Create: `mail/mail.go`
- Test: `mail/mail_test.go`

**Interfaces:**
- Produces: `mail.Sender` interface `{ Send(to, subject, body string) error }`; `mail.SMTPSender{Host, Port, User, Password, From string}`; `mail.NewSMTPSenderFromEnv() (*SMTPSender, bool)`; `mail.Message(from, to, subject, body string, now time.Time) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

`mail/mail_test.go`:

```go
package mail_test

import (
	"strings"
	"testing"
	"time"

	"goblog/mail"
)

func TestMessage_HeadersAndCRLF(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	msg, err := mail.Message("blog@example.com", "reader@example.com", "Your code", "line one\nline two\n", now)
	if err != nil {
		t.Fatal(err)
	}
	got := string(msg)
	headers, body, found := strings.Cut(got, "\r\n\r\n")
	if !found {
		t.Fatalf("expected a blank line between headers and body:\n%s", got)
	}
	for _, want := range []string{
		"From: blog@example.com\r\n",
		"To: reader@example.com\r\n",
		"Subject: Your code\r\n",
		"Date: Sat, 12 Sep 2026 10:00:00 +0000\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
	} {
		if !strings.Contains(headers+"\r\n", want) {
			t.Errorf("missing header %q in:\n%s", want, headers)
		}
	}
	if body != "line one\r\nline two\r\n" {
		t.Errorf("body must use CRLF, got %q", body)
	}
	if strings.Contains(got, "\r\n\r\n\r\n") || strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("bare LF or doubled CRLF in message: %q", got)
	}
}

func TestMessage_NonASCIISubjectIsEncoded(t *testing.T) {
	msg, err := mail.Message("a@example.com", "b@example.com", "Your Café login code", "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Subject: =?utf-8?q?") {
		t.Errorf("expected RFC 2047 encoded subject, got:\n%s", msg)
	}
}

func TestMessage_RejectsHeaderInjection(t *testing.T) {
	for _, bad := range []string{"x\r\nBcc: victim@example.com", "x\nBcc: y"} {
		if _, err := mail.Message("a@example.com", bad, "s", "b", time.Now()); err == nil {
			t.Errorf("expected error for To=%q", bad)
		}
		if _, err := mail.Message("a@example.com", "b@example.com", bad, "b", time.Now()); err == nil {
			t.Errorf("expected error for Subject=%q", bad)
		}
		if _, err := mail.Message(bad, "b@example.com", "s", "b", time.Now()); err == nil {
			t.Errorf("expected error for From=%q", bad)
		}
	}
}

func TestNewSMTPSenderFromEnv(t *testing.T) {
	clear := func(t *testing.T) {
		for _, k := range []string{"smtp_host", "smtp_port", "smtp_user", "smtp_password", "smtp_from"} {
			t.Setenv(k, "")
		}
	}

	t.Run("unset", func(t *testing.T) {
		clear(t)
		if s, ok := mail.NewSMTPSenderFromEnv(); ok || s != nil {
			t.Fatalf("expected not configured, got %+v %v", s, ok)
		}
	})
	t.Run("host without from", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", "smtp.example.com")
		if _, ok := mail.NewSMTPSenderFromEnv(); ok {
			t.Fatal("smtp_from is required")
		}
	})
	t.Run("defaults port to 587", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", " smtp.example.com ")
		t.Setenv("smtp_from", "blog@example.com")
		s, ok := mail.NewSMTPSenderFromEnv()
		if !ok {
			t.Fatal("expected configured")
		}
		if s.Host != "smtp.example.com" || s.Port != "587" || s.From != "blog@example.com" || s.User != "" {
			t.Fatalf("unexpected sender %+v", s)
		}
	})
	t.Run("all keys", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", "smtp.example.com")
		t.Setenv("smtp_port", "465")
		t.Setenv("smtp_user", "u")
		t.Setenv("smtp_password", "p")
		t.Setenv("smtp_from", "blog@example.com")
		s, _ := mail.NewSMTPSenderFromEnv()
		want := mail.SMTPSender{Host: "smtp.example.com", Port: "465", User: "u", Password: "p", From: "blog@example.com"}
		if *s != want {
			t.Fatalf("want %+v, got %+v", want, *s)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test goblog/mail`
Expected: FAIL — package `goblog/mail` not found.

- [ ] **Step 3: Implement `mail/mail.go`**

```go
// Package mail sends plain-text email over SMTP. Auth uses it to deliver
// one-time login codes; the Sender interface lets tests substitute a fake.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Sender delivers a plain-text email.
type Sender interface {
	Send(to, subject, body string) error
}

// SMTPSender sends through a single SMTP server. Port 465 uses implicit TLS;
// any other port uses STARTTLS when the server offers it. PLAIN auth is used
// only when User is set, so an unauthenticated local relay works.
type SMTPSender struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
}

// NewSMTPSenderFromEnv builds a sender from smtp_host, smtp_port (default
// 587), smtp_user, smtp_password and smtp_from. ok is false when smtp_host or
// smtp_from is unset, meaning email login is not configured.
func NewSMTPSenderFromEnv() (*SMTPSender, bool) {
	host := strings.TrimSpace(os.Getenv("smtp_host"))
	from := strings.TrimSpace(os.Getenv("smtp_from"))
	if host == "" || from == "" {
		return nil, false
	}
	port := strings.TrimSpace(os.Getenv("smtp_port"))
	if port == "" {
		port = "587"
	}
	return &SMTPSender{
		Host:     host,
		Port:     port,
		User:     os.Getenv("smtp_user"),
		Password: os.Getenv("smtp_password"),
		From:     from,
	}, true
}

// Message renders an RFC 5322 plain-text message with CRLF line endings.
// Header values containing a line break are rejected (header injection).
func Message(from, to, subject, body string, now time.Time) ([]byte, error) {
	for _, v := range []string{from, to, subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("mail: header value contains a line break")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String()), nil
}

func (s *SMTPSender) auth() smtp.Auth {
	if s.User == "" {
		return nil
	}
	return smtp.PlainAuth("", s.User, s.Password, s.Host)
}

// Send delivers one message to a single recipient.
func (s *SMTPSender) Send(to, subject, body string) error {
	msg, err := Message(s.From, to, subject, body, time.Now())
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.Host, s.Port)
	if s.Port != "465" {
		return smtp.SendMail(addr, s.auth(), s.From, []string{to}, msg)
	}

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host})
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if a := s.auth(); a != nil {
		if err := c.Auth(a); err != nil {
			return err
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
```

- [ ] **Step 4: Run tests**

Run: `go test goblog/mail -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add mail/mail.go mail/mail_test.go
git commit -m "Add mail package: SMTP sender configured from .env (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `POST /api/login/email` — issue a code

**Files:**
- Modify: `auth/auth.go` (`Auth` struct gets `Mailer`; `EmailLoginEnabled`)
- Create: `auth/otp.go`
- Create: `auth/otp_test.go`

**Interfaces:**
- Consumes: `mail.Sender` (Task 3), `LoginCode` (Task 1).
- Produces: `Auth.Mailer mail.Sender` (nil = disabled); `func EmailLoginEnabled() bool`; `func (a *Auth) SendLoginCodeHandler(c *gin.Context)`; unexported helpers `normalizeEmail`, `generateLoginCode`, `hashLoginCode`, `siteTitle`, and the constants `loginCodeLength`, `loginCodeLifetime`, `loginCodeMaxAttempts`, `loginCodeResendAfter` used by Task 5.

- [ ] **Step 1: Write the failing tests**

`auth/otp_test.go`:

```go
package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"goblog/auth"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// fakeMailer records sends and can be told to fail.
type fakeMailer struct {
	sent []sentMail
	err  error
}

type sentMail struct{ to, subject, body string }

func (f *fakeMailer) Send(to, subject, body string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentMail{to, subject, body})
	return nil
}

var codeRe = regexp.MustCompile(`\b[0-9]{6}\b`)

// lastCode pulls the 6-digit code out of the most recent email.
func lastCode(t *testing.T, m *fakeMailer) string {
	t.Helper()
	if len(m.sent) == 0 {
		t.Fatal("no email was sent")
	}
	code := codeRe.FindString(m.sent[len(m.sent)-1].body)
	if code == "" {
		t.Fatalf("no 6-digit code in body %q", m.sent[len(m.sent)-1].body)
	}
	return code
}

// newOTPRouter mounts the OTP handlers plus a /whoami probe that reports
// IsLoggedIn for the request's session cookie.
func newOTPRouter(a *auth.Auth) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("session", cookie.NewStore([]byte("test"))))
	r.POST("/api/login/email", a.SendLoginCodeHandler)
	r.POST("/api/login/email/verify", a.VerifyLoginCodeHandler)
	r.GET("/whoami", func(c *gin.Context) { c.String(http.StatusOK, strconv.FormatBool(a.IsLoggedIn(c))) })
	return r
}

func postForm(r *gin.Engine, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sendCode(r *gin.Engine, email string) *httptest.ResponseRecorder {
	return postForm(r, "/api/login/email", url.Values{"email": {email}}, nil)
}

// newOTPAuth is newAuth with a fake mailer attached.
func newOTPAuth(t *testing.T) (*auth.Auth, *gorm.DB, *fakeMailer) {
	t.Helper()
	a, db := newAuth(t)
	m := &fakeMailer{}
	a.Mailer = m
	return a, db, m
}

func codeCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	db.Model(&auth.LoginCode{}).Count(&n)
	return n
}

func TestSendLoginCode_NotConfigured_404(t *testing.T) {
	a, _ := newAuth(t)
	w := sendCode(newOTPRouter(a), "x@example.com")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without a mailer, got %d %s", w.Code, w.Body)
	}
}

func TestSendLoginCode_BadEmail_400(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	for _, bad := range []string{"", "   ", "no-at-sign", "@example.com", "x@", "a@b@c", strings.Repeat("a", 250) + "@x.io"} {
		w := sendCode(r, bad)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: expected 400, got %d %s", bad, w.Code, w.Body)
		}
	}
	if len(m.sent) != 0 || codeCount(t, db) != 0 {
		t.Fatal("nothing should be sent or stored for invalid addresses")
	}
}

func TestSendLoginCode_SendsCodeAndStoresHash(t *testing.T) {
	a, db, m := newOTPAuth(t)
	db.Exec("CREATE TABLE settings (key text PRIMARY KEY, type text, value text)")
	db.Exec("INSERT INTO settings (key, type, value) VALUES ('site_title', 'text', 'My Blog')")

	w := sendCode(newOTPRouter(a), "  Reader@Example.COM ")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sent"`) {
		t.Fatalf("expected 200 sent, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected 1 email, got %d", len(m.sent))
	}
	if m.sent[0].to != "reader@example.com" {
		t.Errorf("expected normalised recipient, got %q", m.sent[0].to)
	}
	if m.sent[0].subject != "Your My Blog login code" {
		t.Errorf("unexpected subject %q", m.sent[0].subject)
	}
	code := lastCode(t, m)
	if !strings.Contains(m.sent[0].body, "expires in 10 minutes") {
		t.Errorf("body should mention expiry: %q", m.sent[0].body)
	}

	var row auth.LoginCode
	if err := db.First(&row, "email = ?", "reader@example.com").Error; err != nil {
		t.Fatalf("expected a login_codes row: %v", err)
	}
	if row.CodeHash == code || len(row.CodeHash) != 64 {
		t.Errorf("code must be stored as a sha256 hex hash, got %q", row.CodeHash)
	}
	if row.Attempts != 0 {
		t.Errorf("attempts should start at 0, got %d", row.Attempts)
	}
	if until := time.Until(row.ExpiresAt); until < 9*time.Minute || until > 11*time.Minute {
		t.Errorf("expected ~10 minute expiry, got %v", until)
	}
}

func TestSendLoginCode_SiteTitleFallsBackToGoBlog(t *testing.T) {
	a, _, m := newOTPAuth(t) // no settings table in newAuth
	sendCode(newOTPRouter(a), "reader@example.com")
	if m.sent[0].subject != "Your GoBlog login code" {
		t.Errorf("unexpected subject %q", m.sent[0].subject)
	}
}

func TestSendLoginCode_SameResponseForKnownAndUnknownAddress(t *testing.T) {
	a, db, _ := newOTPAuth(t)
	db.Create(&auth.BlogUser{Provider: auth.ProviderEmail, ProviderID: "known@example.com", Login: "known@example.com"})
	r := newOTPRouter(a)
	known := sendCode(r, "known@example.com")
	unknown := sendCode(r, "unknown@example.com")
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("responses differ: %d %s vs %d %s", known.Code, known.Body, unknown.Code, unknown.Body)
	}
}

func TestSendLoginCode_RateLimited_429(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusOK {
		t.Fatalf("first send: %d %s", w.Code, w.Body)
	}
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 inside the resend window, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 1 {
		t.Fatalf("expected the second request not to send, got %d emails", len(m.sent))
	}

	db.Model(&auth.LoginCode{}).Where("email = ?", "reader@example.com").Update("created_at", time.Now().Add(-2*time.Minute))
	if w := sendCode(r, "reader@example.com"); w.Code != http.StatusOK {
		t.Fatalf("expected 200 after the window, got %d %s", w.Code, w.Body)
	}
	if len(m.sent) != 2 || codeCount(t, db) != 1 {
		t.Fatalf("expected a replacement code (2 emails, 1 row), got %d emails, %d rows", len(m.sent), codeCount(t, db))
	}
}

func TestSendLoginCode_PrunesExpiredRows(t *testing.T) {
	a, db, _ := newOTPAuth(t)
	db.Create(&auth.LoginCode{Email: "old@example.com", CodeHash: "x", ExpiresAt: time.Now().Add(-time.Hour), CreatedAt: time.Now().Add(-2 * time.Hour)})
	sendCode(newOTPRouter(a), "new@example.com")
	var n int64
	db.Model(&auth.LoginCode{}).Where("email = ?", "old@example.com").Count(&n)
	if n != 0 {
		t.Fatal("expected the expired row to be pruned")
	}
}

func TestSendLoginCode_MailFailure_500AndNoRow(t *testing.T) {
	a, db, m := newOTPAuth(t)
	m.err = errors.New("smtp down")
	w := sendCode(newOTPRouter(a), "reader@example.com")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "smtp down") {
		t.Errorf("mail error must not leak to the client: %s", w.Body)
	}
	if codeCount(t, db) != 0 {
		t.Fatal("expected no login_codes row after a failed send")
	}
}

func TestEmailLoginEnabled(t *testing.T) {
	t.Setenv("smtp_host", "")
	t.Setenv("smtp_from", "")
	if auth.EmailLoginEnabled() {
		t.Fatal("expected disabled without smtp_host/smtp_from")
	}
	t.Setenv("smtp_host", "smtp.example.com")
	t.Setenv("smtp_from", "blog@example.com")
	if !auth.EmailLoginEnabled() {
		t.Fatal("expected enabled")
	}
}
```

(`VerifyLoginCodeHandler` is referenced by the router; add a stub in this task so the file compiles — Task 5 fills it in.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test goblog/auth`
Expected: compile errors for `a.Mailer`, `SendLoginCodeHandler`, `VerifyLoginCodeHandler`, `EmailLoginEnabled`.

- [ ] **Step 3: Add `Mailer` to `Auth` and `EmailLoginEnabled`**

In `auth/auth.go`, add `"goblog/mail"` to the imports and change the struct and constructor:

```go
// Auth API
type Auth struct {
	db      **gorm.DB // needs a double pointer to be able to update the db
	version string
	// Mailer delivers one-time login codes. nil means email login is not
	// configured and the OTP endpoints respond 404.
	Mailer mail.Sender
}

// New constructs an Auth API
func New(db *gorm.DB, version string) Auth {
	api := Auth{db: &db, version: version}
	return api
}

// EmailLoginEnabled reports whether SMTP is configured in the environment
// (smtp_host and smtp_from), i.e. whether the login page should offer email
// login. main uses the same check to decide whether to set Mailer.
func EmailLoginEnabled() bool {
	_, ok := mail.NewSMTPSenderFromEnv()
	return ok
}
```

- [ ] **Step 4: Create `auth/otp.go`**

```go
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// One-time email login codes (issue #523). A visitor asks for a code, we
// email a 6-digit number and store only its hash; presenting the right code
// within the lifetime logs them in as an email-provider BlogUser.
const (
	loginCodeLength      = 6
	loginCodeLifetime    = 10 * time.Minute
	loginCodeMaxAttempts = 5
	loginCodeResendAfter = 60 * time.Second
	sessionTokenBytes    = 32
)

// normalizeEmail trims and lowercases raw and does a shape check: non-empty,
// at most 254 bytes, exactly one "@" with something on both sides. It is
// deliberately loose; the emailed code is the real proof of ownership.
func normalizeEmail(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 {
		return "", false
	}
	at := strings.Index(e, "@")
	if at <= 0 || at != strings.LastIndex(e, "@") || at == len(e)-1 {
		return "", false
	}
	return e, true
}

func generateLoginCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashLoginCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// siteTitle reads the site_title setting for the email subject. auth cannot
// import blog (blog imports auth), so it queries the settings table directly
// and falls back to "GoBlog" when the table or value is missing.
func (a *Auth) siteTitle() string {
	var title string
	err := (*a.db).Table("settings").Where("key = ?", "site_title").Select("value").Scan(&title).Error
	if err != nil || strings.TrimSpace(title) == "" {
		return "GoBlog"
	}
	return title
}

// SendLoginCodeHandler handles POST /api/login/email (form field: email).
// It always answers 200 {"status":"sent"} for a well-formed address whether
// or not that address has logged in before, so it cannot be used to probe
// for accounts. Requests inside loginCodeResendAfter of the previous one for
// the same address get 429.
func (a *Auth) SendLoginCodeHandler(c *gin.Context) {
	if a.Mailer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email login not configured"})
		return
	}
	email, ok := normalizeEmail(c.PostForm("email"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email address"})
		return
	}
	now := time.Now()
	(*a.db).Where("expires_at < ?", now).Delete(&LoginCode{})

	var existing LoginCode
	if err := (*a.db).First(&existing, "email = ?", email).Error; err == nil && now.Sub(existing.CreatedAt) < loginCodeResendAfter {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "please wait before requesting another code"})
		return
	}

	code, err := generateLoginCode()
	if err != nil {
		log.Printf("generating login code: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	row := LoginCode{Email: email, CodeHash: hashLoginCode(code), ExpiresAt: now.Add(loginCodeLifetime), Attempts: 0, CreatedAt: now}
	if err := (*a.db).Save(&row).Error; err != nil {
		log.Printf("storing login code for %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}

	subject := fmt.Sprintf("Your %s login code", a.siteTitle())
	body := fmt.Sprintf("Your login code is %s. It expires in 10 minutes.\n\nIf you did not request this, ignore this email.\n", code)
	if err := a.Mailer.Send(email, subject, body); err != nil {
		log.Printf("sending login code to %s: %v", email, err)
		(*a.db).Delete(&LoginCode{}, "email = ?", email)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not send the login code"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

// VerifyLoginCodeHandler handles POST /api/login/email/verify. Implemented in
// the next step of the plan.
func (a *Auth) VerifyLoginCodeHandler(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "not implemented"})
}
```

- [ ] **Step 5: Run tests**

Run: `go test goblog/auth -run 'TestSendLoginCode|TestEmailLoginEnabled' -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add auth/auth.go auth/otp.go auth/otp_test.go
git commit -m "Issue one-time email login codes (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `POST /api/login/email/verify` — redeem a code and log in

**Files:**
- Modify: `auth/otp.go` (replace the stub)
- Modify: `auth/otp_test.go`

**Interfaces:**
- Consumes: `UpsertUser` (Task 2), helpers and constants from Task 4.
- Produces: `func (a *Auth) VerifyLoginCodeHandler(c *gin.Context)`.

- [ ] **Step 1: Write the failing tests**

Append to `auth/otp_test.go`:

```go
func verify(r *gin.Engine, email, code string) *httptest.ResponseRecorder {
	return postForm(r, "/api/login/email/verify", url.Values{"email": {email}, "code": {code}}, nil)
}

func TestVerifyLoginCode_NotConfigured_404(t *testing.T) {
	a, _ := newAuth(t)
	if w := verify(newOTPRouter(a), "x@example.com", "123456"); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestVerifyLoginCode_MalformedInput_400(t *testing.T) {
	a, _, _ := newOTPAuth(t)
	r := newOTPRouter(a)
	for _, tc := range []struct{ email, code string }{
		{"not-an-email", "123456"},
		{"x@example.com", ""},
		{"x@example.com", "12345"},
		{"x@example.com", "1234567"},
		{"x@example.com", "12a456"},
		{"x@example.com", "１２３４５６"},
	} {
		if w := verify(r, tc.email, tc.code); w.Code != http.StatusBadRequest {
			t.Errorf("%+v: expected 400, got %d %s", tc, w.Code, w.Body)
		}
	}
}

func TestVerifyLoginCode_UnknownEmail_401(t *testing.T) {
	a, _, _ := newOTPAuth(t)
	if w := verify(newOTPRouter(a), "nobody@example.com", "123456"); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", w.Code, w.Body)
	}
}

func TestVerifyLoginCode_Success_LogsInAndCreatesUser(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	sendCode(r, "Reader@Example.com")
	code := lastCode(t, m)

	w := verify(r, "reader@example.com", code)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "access_token") {
		t.Errorf("response must not include the session token: %s", w.Body)
	}

	var user auth.BlogUser
	if err := db.First(&user, "provider = ? AND provider_id = ?", auth.ProviderEmail, "reader@example.com").Error; err != nil {
		t.Fatalf("expected an email user row: %v", err)
	}
	if user.Login != "reader@example.com" || user.Email != "reader@example.com" {
		t.Errorf("unexpected user %+v", user)
	}
	if len(user.AccessToken) != 64 {
		t.Errorf("expected a 32-byte hex session token, got %q", user.AccessToken)
	}
	if codeCount(t, db) != 0 {
		t.Error("expected the code row to be deleted after use")
	}

	// The session cookie from the verify response must make IsLoggedIn true.
	req := httptest.NewRequest("GET", "/whoami", nil)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	who := httptest.NewRecorder()
	r.ServeHTTP(who, req)
	if who.Body.String() != "true" {
		t.Fatalf("expected the session to be logged in, got %q", who.Body.String())
	}
}

func TestVerifyLoginCode_ReplayFails(t *testing.T) {
	a, _, m := newOTPAuth(t)
	r := newOTPRouter(a)
	sendCode(r, "reader@example.com")
	code := lastCode(t, m)
	if w := verify(r, "reader@example.com", code); w.Code != http.StatusOK {
		t.Fatalf("first verify: %d %s", w.Code, w.Body)
	}
	if w := verify(r, "reader@example.com", code); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected replay to fail with 401, got %d %s", w.Code, w.Body)
	}
}

func TestVerifyLoginCode_SecondLogin_ReusesUserAndRotatesToken(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)

	sendCode(r, "reader@example.com")
	verify(r, "reader@example.com", lastCode(t, m))
	var first auth.BlogUser
	db.First(&first, "provider_id = ?", "reader@example.com")

	db.Model(&auth.LoginCode{}).Where("email = ?", "reader@example.com").Update("created_at", time.Now().Add(-2*time.Minute))
	sendCode(r, "reader@example.com")
	if w := verify(r, "reader@example.com", lastCode(t, m)); w.Code != http.StatusOK {
		t.Fatalf("second verify: %d %s", w.Code, w.Body)
	}
	var second auth.BlogUser
	db.First(&second, "provider_id = ?", "reader@example.com")

	var users int64
	db.Model(&auth.BlogUser{}).Count(&users)
	if users != 1 || second.ID != first.ID {
		t.Fatalf("expected one user reused, got %d users, ids %d/%d", users, first.ID, second.ID)
	}
	if second.AccessToken == first.AccessToken {
		t.Fatal("expected the session token to rotate on each login")
	}
}

func TestVerifyLoginCode_WrongCode_401AndCountsAttempt(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	sendCode(r, "reader@example.com")
	code := lastCode(t, m)
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}
	if w := verify(r, "reader@example.com", wrong); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", w.Code, w.Body)
	}
	var row auth.LoginCode
	db.First(&row, "email = ?", "reader@example.com")
	if row.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", row.Attempts)
	}
	if w := verify(r, "reader@example.com", code); w.Code != http.StatusOK {
		t.Fatalf("the right code should still work after one miss, got %d %s", w.Code, w.Body)
	}
}

func TestVerifyLoginCode_TooManyAttempts_LocksCode(t *testing.T) {
	a, _, m := newOTPAuth(t)
	r := newOTPRouter(a)
	sendCode(r, "reader@example.com")
	code := lastCode(t, m)
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		verify(r, "reader@example.com", wrong)
	}
	if w := verify(r, "reader@example.com", code); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected the right code to be refused after 5 misses, got %d %s", w.Code, w.Body)
	}
}

func TestVerifyLoginCode_Expired_401(t *testing.T) {
	a, db, m := newOTPAuth(t)
	r := newOTPRouter(a)
	sendCode(r, "reader@example.com")
	code := lastCode(t, m)
	db.Model(&auth.LoginCode{}).Where("email = ?", "reader@example.com").Update("expires_at", time.Now().Add(-time.Second))
	if w := verify(r, "reader@example.com", code); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an expired code, got %d %s", w.Code, w.Body)
	}
}

func TestVerifyLoginCode_NeverPromotesToAdmin(t *testing.T) {
	a, db, m := newOTPAuth(t) // newAuth clears admin_login/admin_github_id: first-to-login would win for GitHub
	r := newOTPRouter(a)
	sendCode(r, "reader@example.com")
	if w := verify(r, "reader@example.com", lastCode(t, m)); w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	if adminCount(t, db) != 0 {
		t.Fatal("an email login must never create an admin")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test goblog/auth -run TestVerifyLoginCode -v`
Expected: FAIL — handlers return 501.

- [ ] **Step 3: Implement the handler**

In `auth/otp.go` add imports `"crypto/subtle"` and `"github.com/gin-contrib/sessions"`, then replace the stub:

```go
func isLoginCode(s string) bool {
	if len(s) != loginCodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func newSessionToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// VerifyLoginCodeHandler handles POST /api/login/email/verify (form fields:
// email, code). A missing, expired, locked or wrong code is a 401 with the
// same body. Success deletes the code, creates or refreshes the email user
// with a fresh random session token, and stores that token in the session
// exactly as the GitHub flow does, so IsLoggedIn works unchanged. Admin
// promotion is deliberately not attempted here.
func (a *Auth) VerifyLoginCodeHandler(c *gin.Context) {
	if a.Mailer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email login not configured"})
		return
	}
	email, ok := normalizeEmail(c.PostForm("email"))
	code := strings.TrimSpace(c.PostForm("code"))
	if !ok || !isLoginCode(code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email or code"})
		return
	}

	var row LoginCode
	err := (*a.db).First(&row, "email = ?", email).Error
	if err != nil || time.Now().After(row.ExpiresAt) || row.Attempts >= loginCodeMaxAttempts {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(hashLoginCode(code)), []byte(row.CodeHash)) != 1 {
		(*a.db).Model(&row).Update("attempts", row.Attempts+1)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}
	(*a.db).Delete(&LoginCode{}, "email = ?", email)

	token, err := newSessionToken()
	if err != nil {
		log.Printf("generating session token: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}
	user, err := a.UpsertUser(&BlogUser{
		Provider:    ProviderEmail,
		ProviderID:  email,
		Login:       email,
		Email:       email,
		AccessToken: token,
	})
	if err != nil {
		log.Printf("storing email user %s: %v", email, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}

	session := sessions.Default(c)
	session.Set("token", token)
	if err := session.Save(); err != nil {
		log.Printf("saving session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not log in"})
		return
	}
	c.JSON(http.StatusOK, user)
}
```

- [ ] **Step 4: Run tests**

Run: `go test goblog/auth -v`
Expected: all PASS, including the earlier tests.

- [ ] **Step 5: Commit**

```bash
git add auth/otp.go auth/otp_test.go
git commit -m "Verify email login codes and log the visitor in (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Wire it up — mailer, routes, login page flag

**Files:**
- Modify: `goblog.go` (`main`, `addRoutesInner`)
- Modify: `blog/blog.go:1066-1077` (`Login`)

**Interfaces:**
- Consumes: `mail.NewSMTPSenderFromEnv`, `auth.EmailLoginEnabled`, the two handlers.
- Produces: template variable `email_login_enabled` (bool) for `login.html`.

- [ ] **Step 1: Load `.env` into the process environment and build the mailer**

In `goblog.go` `main`, immediately after the `if !envFilePresent() { ... } else { ... }` block that handles `SESSION_KEY` and the database connection (it ends with the `}` that closes the `else`; the next statement is `_auth := auth.New(db, Version)`), add:

```go
	// The wizard and admin pin read settings lazily via os.Getenv after a
	// godotenv.Load; SMTP is needed at startup, so load once here too.
	if err := godotenv.Load(".env"); err != nil {
		log.Println("Couldn't load .env into the environment: " + err.Error())
	}
```

After `_auth := auth.New(db, Version)` add:

```go
	if sender, ok := mail.NewSMTPSenderFromEnv(); ok {
		log.Println("SMTP configured; email login enabled")
		_auth.Mailer = sender
	}
```

Add `"goblog/mail"` to the imports.

- [ ] **Step 2: Register the routes**

In `addRoutesInner`, directly after `g.router.POST("/api/login", g._auth.LoginPostHandler)`:

```go
	g.router.POST("/api/login/email", g._auth.SendLoginCodeHandler)
	g.router.POST("/api/login/email/verify", g._auth.VerifyLoginCodeHandler)
```

- [ ] **Step 3: Pass the flag to the login template**

In `blog/blog.go` `Login`, add to the `gin.H` for `login.html`:

```go
		"email_login_enabled": auth.EmailLoginEnabled(),
```

(`godotenv.Load(".env")` already ran a few lines above, so the env is populated.) `goblog/auth` is already imported in `blog.go`.

- [ ] **Step 4: Build and run the suite**

Run: `go build ./... && go vet ./... && go test goblog/...`
Expected: build OK, all PASS.

- [ ] **Step 5: Commit**

```bash
git add goblog.go blog/blog.go
git commit -m "Wire SMTP mailer and email login routes into the server (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Login page email form in all three themes

**Files:**
- Modify: `themes/default/templates/login.html`
- Modify: `themes/forest/templates/login.html`
- Modify: `themes/minimal/templates/login.html`

**Interfaces:**
- Consumes: `email_login_enabled`, `POST /api/login/email`, `POST /api/login/email/verify` (JSON `{"error": ...}` on failure).

- [ ] **Step 1: Add the shared script to each template**

In each of the three files, inside the existing `<script type="text/javascript">` block at the top (after the `if (code) { ... }` block, still inside the script), add:

```javascript
  $(function () {
    var emailForm = $("#email-form"), codeForm = $("#code-form"),
        status = $("#email-login-status"), change = $("#email-change");
    if (!emailForm.length) { return; }
    function showError(xhr, fallback) {
      status.text((xhr.responseJSON && xhr.responseJSON.error) || fallback);
    }
    emailForm.on("submit", function (e) {
      e.preventDefault();
      status.text("");
      $.post(host + "/api/login/email", {email: $("#email-input").val()})
        .done(function () {
          emailForm.hide(); codeForm.css("display", "flex"); change.show();
          status.text("Check your inbox for a 6-digit code.");
          $("#code-input").val("").focus();
        })
        .fail(function (xhr) { showError(xhr, "Could not send the code."); });
    });
    codeForm.on("submit", function (e) {
      e.preventDefault();
      status.text("");
      $.post(host + "/api/login/email/verify", {email: $("#email-input").val(), code: $("#code-input").val()})
        .done(function () { window.location.href = "/"; })
        .fail(function (xhr) { showError(xhr, "Could not verify the code."); });
    });
    change.on("click", function (e) {
      e.preventDefault();
      codeForm.hide(); change.hide(); emailForm.show(); status.text("");
    });
  });
```

- [ ] **Step 2: Add the markup — default theme**

In `themes/default/templates/login.html`, directly after the `<script>$("#github-button").prop(...)</script>` line and before the following `<div class="clear">`:

```html
      {{ if .email_login_enabled }}
      <div class="clear">&nbsp;</div>
      <p class="text-muted">or sign in with email</p>
      <form id="email-form" class="form-inline justify-content-center">
        <input type="email" id="email-input" class="form-control mr-2" placeholder="you@example.com" autocomplete="email" required>
        <button type="submit" class="btn btn-primary">Send code</button>
      </form>
      <form id="code-form" class="form-inline justify-content-center" style="display:none">
        <input type="text" id="code-input" class="form-control mr-2" placeholder="6-digit code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required>
        <button type="submit" class="btn btn-primary">Verify</button>
      </form>
      <p id="email-login-status" class="mt-2"></p>
      <a href="#" id="email-change" style="display:none">use a different email</a>
      {{ end }}
```

- [ ] **Step 3: Add the markup — forest theme**

In `themes/forest/templates/login.html`, after the `<script>$("#github-button")...</script>` line, inside the `.glass-card` div:

```html
    {{ if .email_login_enabled }}
    <p style="color: #7a937a; font-size: 13px; margin: 24px 0 10px;">or sign in with email</p>
    <form id="email-form" style="display: flex; gap: 8px; justify-content: center; flex-wrap: wrap;">
      <input type="email" id="email-input" placeholder="you@example.com" autocomplete="email" required
             style="flex: 1 1 200px; padding: 10px 12px; border: 1px solid #c5d6c5; border-radius: 8px; font-size: 14px;">
      <button type="submit" style="padding: 10px 18px; background: #2d5a27; color: #fff; border: 0; border-radius: 8px; font-size: 14px; font-weight: 500;">Send code</button>
    </form>
    <form id="code-form" style="display: none; gap: 8px; justify-content: center; flex-wrap: wrap;">
      <input type="text" id="code-input" placeholder="6-digit code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required
             style="flex: 1 1 200px; padding: 10px 12px; border: 1px solid #c5d6c5; border-radius: 8px; font-size: 14px; letter-spacing: 0.2em;">
      <button type="submit" style="padding: 10px 18px; background: #2d5a27; color: #fff; border: 0; border-radius: 8px; font-size: 14px; font-weight: 500;">Verify</button>
    </form>
    <p id="email-login-status" style="color: #7a937a; font-size: 13px; margin-top: 12px; min-height: 1em;"></p>
    <a href="#" id="email-change" style="display: none; color: #2d5a27; font-size: 13px;">use a different email</a>
    {{ end }}
```

- [ ] **Step 4: Add the markup — minimal theme**

In `themes/minimal/templates/login.html`, after the `<script>$("#github-button")...</script>` line:

```html
    {{ if .email_login_enabled }}
    <p style="color: #64748b; font-size: 13px; margin: 24px 0 10px;">or sign in with email</p>
    <form id="email-form" style="display: flex; gap: 8px; justify-content: center; flex-wrap: wrap;">
      <input type="email" id="email-input" placeholder="you@example.com" autocomplete="email" required
             style="flex: 1 1 200px; padding: 9px 12px; border: 1px solid #cbd5e1; border-radius: 6px; font-size: 14px;">
      <button type="submit" style="padding: 9px 16px; background: #24292e; color: #fff; border: 0; border-radius: 6px; font-size: 14px; font-weight: 500;">Send code</button>
    </form>
    <form id="code-form" style="display: none; gap: 8px; justify-content: center; flex-wrap: wrap;">
      <input type="text" id="code-input" placeholder="6-digit code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required
             style="flex: 1 1 200px; padding: 9px 12px; border: 1px solid #cbd5e1; border-radius: 6px; font-size: 14px; letter-spacing: 0.2em;">
      <button type="submit" style="padding: 9px 16px; background: #24292e; color: #fff; border: 0; border-radius: 6px; font-size: 14px; font-weight: 500;">Verify</button>
    </form>
    <p id="email-login-status" style="color: #64748b; font-size: 13px; margin-top: 12px; min-height: 1em;"></p>
    <a href="#" id="email-change" style="display: none; color: #24292e; font-size: 13px;">use a different email</a>
    {{ end }}
```

- [ ] **Step 5: Smoke-test the rendered page**

Run the server from a scratch copy so no `.env` or sqlite file lands in the repo:

```bash
S=/tmp/claude-1000/-home-jason-dev-goblog/903e6828-351c-40d7-a141-b957d18e9ad1/scratchpad/smoke
rm -rf "$S" && mkdir -p "$S" && cp -r themes www templates "$S"/ && go build -o "$S/goblog" . && cd "$S"
printf 'database=sqlite\nsqlite_db=smoke.db\nclient_id=x\nclient_secret=y\nsmtp_host=localhost\nsmtp_from=blog@example.com\n' > .env
./goblog > server.log 2>&1 &
sleep 3
curl -s localhost:7000/ > /dev/null   # triggers migration + route registration
curl -s localhost:7000/login | grep -c 'id="email-form"'          # expect 1
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d 'email=nope' localhost:7000/api/login/email   # expect 400
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d 'email=a@b.co' localhost:7000/api/login/email # expect 500 (no SMTP listening) and a log line
kill %1
sed -i '/^smtp_/d' .env && ./goblog > server.log 2>&1 & sleep 3
curl -s localhost:7000/ > /dev/null
curl -s localhost:7000/login | grep -c 'id="email-form"'          # expect 0
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d 'email=a@b.co' localhost:7000/api/login/email # expect 404
kill %1; cd /home/jason/dev/goblog
```

Repeat the `grep -c 'id="email-form"'` check for the other two themes by setting `theme` in the settings table (`sqlite3 "$S/smoke.db" "UPDATE settings SET value='forest' WHERE key='theme'"` then restart), or by reading the templates carefully if `sqlite3` isn't installed. Expected: the form is present exactly when SMTP is configured, on every theme, and the page renders without a template error in `server.log`.

- [ ] **Step 6: Commit**

```bash
git add themes/default/templates/login.html themes/forest/templates/login.html themes/minimal/templates/login.html
git commit -m "Offer email login on the login page when SMTP is configured (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Configuration docs

**Files:**
- Modify: `template.env`
- Modify: `README.md` (after the "Pinning the Admin Account" section)

- [ ] **Step 1: `template.env`**

Append:

```
# optional: email (one-time code) login for visitors. Enabled when both
# smtp_host and smtp_from are set. smtp_port 465 uses implicit TLS; any other
# port (default 587) uses STARTTLS when the server offers it. Leave smtp_user
# unset for a relay that does not need authentication. Admin login stays
# GitHub-only.
# smtp_host=smtp.example.com
# smtp_port=587
# smtp_user=postmaster@example.com
# smtp_password=<smtp password>
# smtp_from=blog@example.com
```

- [ ] **Step 2: README**

After the "Pinning the Admin Account" section (before `## Theming`) add:

```markdown
### Email Login (one-time codes)
Visitors without a GitHub account can log in with an emailed 6-digit code. Add SMTP details to `.env`:
```bash
smtp_host=smtp.example.com
smtp_port=587                         # 465 for implicit TLS; anything else uses STARTTLS when offered
smtp_user=postmaster@example.com      # omit for an unauthenticated relay
smtp_password=...
smtp_from=blog@example.com
```
When `smtp_host` and `smtp_from` are both set the login page offers "sign in with email"; otherwise it shows GitHub only. Codes expire after 10 minutes, allow 5 wrong attempts, and can be re-requested once a minute. Email users are regular users — the admin account is still GitHub-only (see above).
```

- [ ] **Step 3: Commit and open the PR**

```bash
git add template.env README.md
git commit -m "Document SMTP configuration for email login (#523)

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
git branch --show-current   # must be feat/523-otp-auth
git push -u origin feat/523-otp-auth
```

Then open a PR against `main` with `gh pr create` — title `Add email one-time-code login (#523)`, body summarising: provider pair on `BlogUser` + backfill (incl. postgres sequence), `mail` package, the two endpoints and their limits, login-page form, `access_token` no longer echoed, `IsDbNil` no longer inserts probe rows, and the `.env` keys. Note that admin stays GitHub-only and #524 is the follow-up. End the body with `Closes #523` and `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. Do not merge.
