# Email OTP login (issue #523)

Passwordless "enter your email → we send a code → you're logged in" login,
offered alongside "Sign in with GitHub". It gives commenters without a GitHub
account a verified identity (groundwork for #524). Admin stays GitHub-only.

## Goals

- A visitor can log in with only an email address and access to that inbox.
- Delivery is by SMTP, configured in `.env`. When SMTP is not configured the
  feature is invisible: the login page shows GitHub only and the OTP endpoints
  return 404.
- `BlogUser` becomes provider-agnostic so a future provider is another row
  shape, not another table.
- No change to who can become admin.

## Non-goals

- Comment-form prefill, "verified commenter" badges, or requiring login to
  comment (#524).
- Admin login or admin promotion via email.
- A wizard step or admin UI for SMTP settings.
- Magic links (the code is typed, not clicked).
- Non-SMTP delivery (SendGrid API etc.).

## Identity model

`auth.BlogUser` gains a provider pair and stops using the GitHub id as its
primary key:

```go
type BlogUser struct {
    ID          int    `gorm:"primaryKey" json:"id"`                                       // internal, DB-assigned
    Provider    string `gorm:"uniqueIndex:idx_blog_users_provider;size:32" json:"provider"`  // "github" | "email"
    ProviderID  string `gorm:"uniqueIndex:idx_blog_users_provider;size:255" json:"provider_id"` // github numeric id as a string, or the normalised email
    Login       string `json:"login"`      // github handle, or the email address for email users
    AvatarURL   string `json:"avatar_url"`
    Name        string `json:"name"`
    Email       string `json:"email"`
    AccessToken string `json:"-"`          // session credential: github OAuth token, or a random token for email users
}
```

- `blog_users.id` is already an autoincrement integer on sqlite (via
  `fixBlogUsersTable`), mysql and postgres (created by current GORM). GitHub
  merely supplied explicit ids. No primary-key rewrite is needed.
- GitHub API responses are unmarshalled into a private `githubUser` struct and
  mapped into `BlogUser` with `Provider: "github"`, `ProviderID:
  strconv.Itoa(gh.ID)`, and `ID` left zero so the DB assigns it. Existing
  GitHub users keep their current id; it is now an opaque internal key.
- A single `upsertGitHubUser(gh)` in `auth` looks up `provider='github' AND
  provider_id=?`, creates on miss, updates login/avatar/name/email/token on
  hit, and returns the stored row. Both `LoginPostHandler` and the wizard's
  `createAdminUser` use it (the wizard currently does a bare `Create`, which
  would fail for a returning user).
- `isConfiguredAdmin` compares `admin_github_id` against `ProviderID` and only
  when `Provider == "github"`. `admin_login` behaviour is unchanged.
- `EnsureAdmin` is called only from the GitHub path. An email user is never
  promoted, including on a fresh install with no `admin_login` /
  `admin_github_id` pin.
- `AccessToken` gets `json:"-"`. Today `/api/login` echoes the GitHub OAuth
  token back to the browser in the JSON body; the session cookie is the only
  thing the browser needs.
- `AdminUser.BlogUserID` continues to reference the internal id.

### Migration (`tools/migrate.go`)

Runs after `AutoMigrate`, dialect-neutral SQL:

```sql
UPDATE blog_users SET provider = 'github', provider_id = <id cast to string>
WHERE provider = '' OR provider IS NULL;
```

The cast is `CAST(id AS TEXT)` on sqlite/postgres and `CAST(id AS CHAR)` on
mysql; choose by `db.Dialector.Name()`.

On postgres only, afterwards:

```sql
SELECT setval(pg_get_serial_sequence('blog_users', 'id'),
              COALESCE((SELECT MAX(id) FROM blog_users), 0) + 1, false);
```

Postgres does not advance a sequence on explicit-id inserts, so without this
the first DB-assigned id would be 1 and could collide with an existing row.
MySQL and sqlite already track explicit inserts.

Blank rows inserted by `wizard.IsDbNil` (a pre-existing probe that creates an
empty `BlogUser` on every call) are backfilled as github too. Harmless; not
fixed here.

## Configuration

All optional, in `.env`, documented in `template.env` and the README next to
the `admin_login` block:

| key             | meaning                                   | default |
|-----------------|-------------------------------------------|---------|
| `smtp_host`     | SMTP server hostname                      | —       |
| `smtp_port`     | 465 = implicit TLS; anything else = STARTTLS when offered | 587 |
| `smtp_user`     | auth username; PLAIN auth only when set   | —       |
| `smtp_password` | auth password                             | —       |
| `smtp_from`     | From address on the code email            | —       |

Email login is enabled iff `smtp_host` and `smtp_from` are both non-empty.
Both the login page and the OTP endpoints derive "enabled" from the same
place: `Auth.EmailLoginEnabled()` returns `Mailer != nil`, and `main` sets
`Mailer` from the env once at startup via `mail.NewSMTPSenderFromEnv()`.
Neither reads the env again afterwards, so the login page and the endpoints
can never disagree at runtime — but it also means changing `smtp_*` in
`.env` requires restarting goblog to take effect.

## Mail package (`mail/`)

```go
type Sender interface {
    Send(to, subject, body string) error
}

type SMTPSender struct { Host, Port, User, Password, From string }
func NewSMTPSenderFromEnv() (*SMTPSender, bool) // ok=false when not configured
```

- Port 465: `tls.Dial`, `smtp.NewClient`, auth, send.
- Any other port: `smtp.SendMail`, which negotiates STARTTLS when the server
  advertises it.
- `smtp.PlainAuth` only when `User != ""`, so an unauthenticated local relay
  works.
- Message is plain text with `From`, `To`, `Subject`, `Date`, `MIME-Version`,
  `Content-Type: text/plain; charset=utf-8` headers and CRLF line endings.
  Header values are rejected if they contain `\r` or `\n`.
- `Auth` gets a `Mailer mail.Sender` field, set in `main` when configured.
  Tests inject a fake that records sends and can return an error.

## OTP storage

New table via AutoMigrate:

```go
type LoginCode struct {
    Email     string    `gorm:"primaryKey;size:254"`
    CodeHash  string    `gorm:"size:64"` // hex sha256 of the 6-digit code
    ExpiresAt time.Time
    Attempts  int
    CreatedAt time.Time
}
```

One outstanding code per email; a new request replaces the row.

Constants: code length 6 digits, lifetime 10 minutes, max 5 wrong attempts,
minimum 60 seconds between sends to the same address.

## Endpoints

Registered in `addRoutesInner`. Both return `404 {"error": "email login not
configured"}` when `Mailer == nil`.

### `POST /api/login/email` — form field `email`

1. Normalise: trim, lowercase. Reject (400) if empty, > 254 chars, without
   exactly one `@` with non-empty sides, or containing a space, tab, `,`,
   `<`, `>`, `;`, `"`, `(` or `)`.
2. Per-IP rate limit: an in-memory `ipLimiter` (`auth/otp.go`) allows at most
   `loginCodeSendsPerIP` (5) requests per client IP (`c.ClientIP()`) per
   `loginCodeSendsWindow` (10 minutes), sliding window. Over the limit → 429
   `{"error": "too many requests; try again later"}`. This is independent of
   and checked before the per-address window below, so it also catches
   requests for many distinct addresses from one IP (mail-bombing third
   parties, burning SMTP quota) that the per-address check alone would miss.
   The limiter lives on `*Auth` (`sendLimiter *ipLimiter`, allocated in `New`)
   rather than embedded by value, since `Auth` is copied around.
3. Delete rows with `expires_at < now` (opportunistic pruning).
4. If a row exists for this email with `created_at > now - 60s` → 429
   `{"error": "please wait before requesting another code"}`.
5. Generate a 6-digit code from `crypto/rand` (zero-padded), sha256 it, upsert
   the row with `expires_at = now + 10m`, `attempts = 0`, `created_at = now`.
6. Send `"Your <site_title> login code"` with body
   `"Your login code is 123456. It expires in 10 minutes.\n\nIf you did not
   request this, ignore this email."` `auth` cannot import `blog` (cycle), so the site
   title is read directly: `db.Table("settings").Where("key = ?",
   "site_title").Select("value")`; fall back to "GoBlog" on error or empty.
7. On mail failure: log the error, delete the row (logging that error too if
   it fails), return 500 `{"error": "could not send the login code"}`.
8. Success: 200 `{"status": "sent"}`. The body is identical whether or not the
   address has logged in before.

### `POST /api/login/email/verify` — form fields `email`, `code`

1. Normalise email as above; code must be exactly 6 ASCII digits (400).
2. Load the row. Missing, expired, or `attempts >= 5` → 401
   `{"error": "invalid or expired code"}`.
3. Compare `sha256(code)` with `subtle.ConstantTimeCompare`. Mismatch →
   increment `attempts`, 401 with the same body.
4. Match → delete the row; find-or-create `BlogUser{Provider: "email",
   ProviderID: email, Login: email, Email: email}`; set `AccessToken` to 32
   random bytes hex-encoded (regenerated on every login, so the previous
   session is invalidated); save; set `session["token"]`; 200 with the user
   JSON.

`IsLoggedIn` and `IsAdmin` already resolve `session["token"]` against
`access_token`, so no change there. `Logout` is unchanged.

## Login page

`Blog.Login` passes `email_login_enabled` to `login.html`. All three themes
(`default`, `forest`, `minimal`) gain, under the GitHub button and only when
enabled:

- "or sign in with email" divider
- Step 1: email input + "Send code" button. On 200 hide step 1, show step 2.
  On 429/500 show the error text under the form.
- Step 2: code input (`inputmode=numeric`, `maxlength=6`) + "Verify" button
  and a "use a different email" link back to step 1. On 200 redirect to `/`.
  On 401 show the error.

Same jQuery `$.post` style as the existing GitHub handler. No new static
assets.

## Testing

- `auth/auth_test.go` (existing hermetic sqlite pattern):
  - GitHub upsert: new user gets a DB-assigned id and `provider_id` equal to
    the GitHub id; returning user is matched by `provider_id` and has fields
    refreshed; `admin_github_id` pin matches against `provider_id`;
    `admin_login` pin unchanged.
  - `/api/login/email`: 404 when unconfigured; 400 on bad email; 200 with
    identical body for new and existing addresses; 429 inside the 60 s window;
    500 and no leftover row when the mailer fails; fake mailer receives the
    right recipient and a body containing the code.
  - `/api/login/email/verify`: 400 on malformed code; 401 on unknown email,
    wrong code, expired code, and after 5 wrong attempts; success creates the
    user once (second login reuses the row), rotates `access_token`, sets the
    session, and deletes the code; replaying the same code fails.
  - `/api/login` response body no longer contains `access_token`.
- `tools/migrate_test.go`: seed a `blog_users` row with an explicit id and
  empty provider, run `Migrate`, assert `provider='github'` and
  `provider_id` equals the id as a string; a row that already has a provider
  is untouched.
- `mail/mail_test.go`: message construction (headers, CRLF, charset) and
  rejection of header injection; `NewSMTPSenderFromEnv` returns ok=false
  without `smtp_host`/`smtp_from` and defaults the port to 587. Live SMTP is
  not tested.
- Existing auth and wizard tests are updated for the `ID` → `ProviderID`
  change.

## Files touched

- `auth/user.go` — `BlogUser` fields, `LoginCode`
- `auth/auth.go` — `githubUser`, `upsertGitHubUser`, `isConfiguredAdmin`,
  `Mailer` field, `EmailLoginEnabled`
- `auth/otp.go` — send/verify handlers and helpers
- `auth/auth_test.go`, `auth/otp_test.go`
- `mail/mail.go`, `mail/mail_test.go`
- `tools/migrate.go`, `tools/migrate_test.go` — AutoMigrate list, backfill,
  postgres sequence
- `wizard/wizard.go` — `createAdminUser` via `upsertGitHubUser`
- `blog/blog.go` — `Login` passes `email_login_enabled`
- `goblog.go` — construct mailer, register routes
- `themes/{default,forest,minimal}/templates/login.html`
- `template.env`, `README.md`
