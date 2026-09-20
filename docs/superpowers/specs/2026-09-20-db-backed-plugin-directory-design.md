# Database-backed plugin directory — design

Supersedes the registry half of `2026-09-14-plugin-directory-design.md` and
section 4 (submissions) of `2026-09-17-plugin-install-admin-design.md`. The
plugin repo contract, index format and installer are unchanged.

## Goal

Make goblog.live's database the plugin registry. Publishing a plugin means
submitting a GitHub repo URL on goblog.live and an admin approving it — no
pull request into `goblogplatform/plugins`, no GitHub Pages, no bot token.
Any goblog can host a directory the same way.

## Decisions

- **The compiled-in `directory` plugin is the registry.** It owns the curated
  repo list and the built index in two gorm tables, builds entries itself
  from the GitHub API, and serves `/plugins`, `/plugins/<name>`,
  `/plugins/<name>.json` and `/plugins/index.json` from the DB. The "fetch a
  remote index and mirror it" mode is removed.
- **Submit → admin approves.** Anyone submits; the repo is validated
  synchronously and queued; an admin approves or rejects in Admin → Plugins.
  Submitters get the validation result immediately and nothing else (no
  email, no status page).
- **Validation runs in-process** through `wasm.LoadBytes` — the same Extism
  sandbox (no fs, no network, 64 MB, call timeouts) that runs installed
  plugins. No Docker.
- **`goblogplatform/plugins` is retired.** Its contract doc moves into
  goblog; workflows, tool and Pages go away.
- **Plugins remain GitHub repos with `vX.Y.Z` releases and a `plugin.wasm`
  asset.** The contract and the index/detail JSON consumed by the installer
  are byte-for-byte the same shape as today, so no other goblog changes.

## Architecture

```
plugin author                    goblog.live (directory plugin)                other goblogs
─────────────                    ───────────────────────────────               ────────────
github.com/you/goblog-plugin-x   POST /plugins/submit ─► validate+build ─► directory_repos (pending)
  goblog-plugin.json                                                          directory_builds
  README.md, CHANGELOG.md        Admin → Plugins → Directory: approve  ─► status=approved
  releases vX.Y.Z + plugin.wasm  scheduled rebuild (every refresh_minutes)
                                 /plugins, /plugins/<name>[.json], /plugins/index.json ◄── installer fetch
```

## 1. Data model

Migrated by the plugin's `OnInit` (`db.AutoMigrate`). Curation and cache are
separate tables because they change on different schedules.

`directory_repos` — one row per repository ever submitted:

| column | notes |
|---|---|
| `id` | |
| `repo` | `owner/name`, stored lower-cased, unique |
| `status` | `pending` / `approved` / `rejected` |
| `submitted_at`, `decided_at` | |
| `reject_reason` | shown only in admin |
| `submitter_ip` | kept for the admin; blanked by the scheduled job after 7 days (rate limiting is in memory) |

`directory_builds` — the last successful build of a repo (what
`plugins/<name>.json` held):

| column | notes |
|---|---|
| `repo_id` | unique FK → `directory_repos` |
| `name` | plugin name, unique across builds (name collisions are refused at submit time) |
| `doc` | the detail document as JSON (index fields + README/changelog HTML + releases) |
| `version`, `stars` | copied out of `doc` for the admin list and the unchanged-release check |
| `built_at` | |
| `last_attempt_at`, `last_error` | a failed rebuild keeps the old build and records why |

A submission that passes validation writes both rows (`pending` + build) so
the admin reviews exactly what would be published.

## 2. Builder — `plugins/directory/registry`

Ported from `goblogplatform/plugins/internal/registry` (`manifest.go`,
`validate.go`, `source.go`, `build.go` and their tests); behaviour unchanged
except where noted.

- `Source` interface stays. `GitHubSource` is rewritten on `net/http` (no
  `go-github`): list releases (paged), get contents at ref, download asset
  (follows the redirect, ≤ 16 MiB), `POST /markdown` (gfm, repo context),
  get repo (stars). 30 s timeout on API calls, 2 min on asset downloads,
  `User-Agent: goblog-directory/<version>`. Bearer token when the
  `github_token` setting is set.
- `Validator` interface stays; the implementation is
  `wasm.LoadBytes(module, wasm.Options{})` → `Identity()` → `Close()`,
  reporting `Runtime: "wasm"`. A package-level mutex serializes module
  loads so a burst of submissions cannot stack instances.
- `ValidateEntry(ctx, src, val, repo)` is unchanged: tag `vX.Y.Z`, manifest
  rules (name pattern, entry `.wasm`, `allowed_hosts` rules, license list),
  README present, asset named by `entry` ≤ 16 MiB, module loads, `Name()` ==
  manifest name, `Version()` == tag.
- `BuildRepo(ctx, src, val, repo, baseURL) (Build, error)` replaces `Build`:
  validates and returns one detail doc (index fields + README/changelog HTML
  + releases). `detail_url` is `<baseURL>/plugins/<name>.json`, where
  baseURL is the site's configured URL. Stars failure logs and uses 0, as
  today.

## 3. The `directory` plugin

Settings:

| key | default | notes |
|---|---|---|
| `enabled` | `false` | |
| `refresh_minutes` | `360` | rebuild interval; values below 15 are clamped to 15 |
| `github_token` | `""` | optional; raises the GitHub API limit from 60/h to 5000/h. Type `password` so the admin UI masks it |

`index_url` is removed. `OnInit` migrates the two tables and ensures the
`plugins` page as today.

**Index cache.** The plugin keeps the current `index.json` bytes and the
parsed entries in memory, regenerated (`regenerate()`) after every approve,
reject, delist, submit-as-admin and scheduled rebuild, and on first use.
Only builds whose repo is `approved` are included, sorted by name; the
listing sorts by stars desc, name asc.

**Scheduled job** (the existing minute ticker): when the last run is older
than `refresh_minutes`, for every `approved` repo: list releases; if the
latest valid tag equals the built version, only refresh stars (two API
calls); otherwise run `BuildRepo` and replace the build. Any error sets
`last_attempt_at`/`last_error` and keeps the old build. Then blank
`submitter_ip` older than 7 days and `regenerate()`.

**Pages** (`RenderPage`, by `SubPath`):

- `""` → listing from the cache. No approved builds → "The plugin directory
  is empty" inside the page (200).
- `index.json` → cached bytes, `application/json`,
  `Cache-Control: public, max-age=300`; `[]` when empty (200 — the old 503
  meant "not fetched yet", which no longer exists).
- `<name>` → detail page from `directory_builds` (approved only); unknown →
  404 as today.
- `<name>.json` → the detail doc as JSON (same fields as the old
  `plugins/<name>.json`).
- `submit` → the submission form / handler (below).
- anything else → 404.

**Submission** (`/plugins/submit`):

- `GET` → form: repo URL input, honeypot field, the contract's three
  requirements, link to the contract doc.
- `POST` (form-encoded `repo`, `website` honeypot):
  1. Honeypot filled → render the success page without doing anything.
  2. Parse `https://github.com/<owner>/<repo>[.git][/…]` or bare
     `owner/repo` → lower-cased `owner/repo`; anything else → form with
     "enter a GitHub repository URL".
  3. Existing row: `approved` → "already listed" (link to its page);
     `pending` → "already under review"; `rejected` → proceed (resubmission
     replaces the row's build and sets it back to `pending`).
  4. Rate limit: 5 attempts per hour per client IP, counted in memory
     (every attempt, pass or fail), and one validation at a time (a
     `TryLock`; busy → 429 "try again in a minute"). Validation has a 90 s
     context budget.
  5. `BuildRepo`. Failure → the form again with the error message (the same
     text CI produced today). A plugin `name` already owned by a different
     approved/pending repo → "name is taken".
  6. Success → create/update `directory_repos` (`pending`, `submitted_at`,
     `submitter_ip`) and `directory_builds`; render "Queued for review — it
     appears on this page once approved".

Both GET and POST render through `page_content.html` with `plugin_content`
from embedded templates, so all strings are escaped and only `*_html`
fields are `template.HTML`.

## 4. Admin

`Admin` gains `Directory *directory.Plugin` (nil when not wired; `main`
sets it). A `requireDirectory` helper mirrors `requireInstaller` (401 for
non-admins, 503 when the plugin is not wired or its service is not yet
initialised — not merely when `enabled` is `false`: admin curation works
while the directory is disabled, which is useful for seeding entries before
publishing it).

API:

| method + path | body / query | effect |
|---|---|---|
| `GET /api/v1/directory/repos` | `?status=` (optional) | rows with build summary: `repo`, `status`, `submitted_at`, `decided_at`, `reject_reason`, and from the build `name`, `display_name`, `version`, `author`, `license`, `stars`, `allowed_hosts`, `built_at`, `last_error` |
| `GET /api/v1/directory/repos/:id` | | the row plus the full detail doc (README HTML etc.) for the review view |
| `POST /api/v1/directory/repos` | `{repo}` | admin submission: validate + build, status `approved` directly. Same parse/name-collision rules as public submit, no rate limit |
| `POST /api/v1/directory/repos/:id/approve` | | `pending`/`rejected` → `approved` |
| `POST /api/v1/directory/repos/:id/reject` | `{reason}` | → `rejected` |
| `POST /api/v1/directory/repos/:id/rebuild` | | `BuildRepo` now; error returned and stored in `last_error` |
| `DELETE /api/v1/directory/repos/:id` | | delist: deletes the repo row and its build |

Every mutation calls `regenerate()`. Validation errors map to 400/409/429;
unknown id 404; everything else 500 + log. `GET /api/v1/plugins/status`
gains `directory_hosted: bool` (directory plugin wired and enabled).

UI: `admin_plugins.html` gets a third tab, **Directory**, rendered only when
`directory_hosted` is true.
- "Add repository" box at the top (the admin path; how hello and scholar
  are seeded).
- **Pending**: cards with display name, name/version, author, license,
  allowed hosts, repo link, `Requires goblog ≥ x`, expandable README HTML;
  Approve, Reject (prompts for a reason).
- **Approved**: table with name, version, ★ stars, built time, last error
  (if any); Rebuild, Delist (confirm).
- **Rejected**: repo, reason, date; Approve (re-review) or Delete.
Same vanilla-JS pattern as the other tabs: buttons disable during requests,
server messages render inline, no reloads. The `goblog-site-theme` copy of
the template is a separate PR.

These endpoints are covered by the CSRF follow-up (#571) like the existing
plugin endpoints.

## 5. Installer

The wire types (`Entry`, `Detail`, `Release`) live in `plugins/directory/registry`
and are aliased in `plugins/directory` so both packages share one definition;
only `directory.Fetcher` moves, to `plugin/installer/fetcher.go` (tests move
with it). `DefaultIndexURL` stays `https://www.goblog.live/plugins/index.json`.
No behaviour change.

## 6. Retirement and deployment

- `goblogplatform/plugins`: PR removing `registry.yaml`, `cmd/`, `internal/`,
  `dist/`, `go.mod`/`go.sum`, `renovate.json`, the three workflows and the
  issue template; README becomes a pointer to `goblog.live/plugins/submit`
  and the contract doc in goblog. After merge: disable Pages, archive the
  repo.
- goblog: `docs/CONTRACT.md` from the registry lands as
  `docs/PLUGIN_CONTRACT.md`, linked from the README's plugin section and the
  submit page. The README's directory section describes the DB-backed flow
  and the `github_token` setting.
- Release `v0.4.0` (the `directory` plugin's public settings change).
  Renovate bumps iac; redeploy goblog.live. Between deploy and seeding
  `index.json` is `[]`; installers see an empty directory, nothing breaks.
  Before seeding for real:
  1. Verify `site_url` is `https://www.goblog.live` (`detail_url` is baked
     from it at build time; changing `site_url` later needs a Rebuild of
     every existing entry to pick it up).
  2. Set `github_token`, or accept the unauthenticated 60 requests/hour
     limit.
  3. Set `refresh_minutes` to 360 if the old default of 15 is still stored
     from an earlier deploy.
  4. Do one browser pass of the Directory tab — Add, Show README, Reject,
     Approve, Rebuild, Delist — before trusting it with real submissions.
  Then Admin → Plugins → Directory → Add `goblogplatform/goblog-plugin-hello`
  and `goblogplatform/goblog-plugin-scholar`.
- No iac change beyond the version bump; no new env vars.

## 7. Testing

- `plugins/directory/registry`: ported table tests against an `httptest`
  GitHub fake (releases, contents, asset redirect + download, markdown,
  repo); the validator against real modules from `plugin/wasm/testdata`
  (identity match, name mismatch, version mismatch, non-wasm bytes).
- `plugins/directory` on sqlite: submit happy path → pending + build; parse
  forms (URL, `.git`, bare `owner/repo`, garbage); already listed / under
  review / resubmit after reject; name taken; rate limit and busy lock;
  honeypot; scheduled rebuild keeps the old build on failure and refreshes
  only stars when the tag is unchanged; `index.json` approved-only and `[]`
  when empty; `<name>.json`; approve/reject/delist regenerate the index;
  `submitter_ip` blanking.
- `admin`: router tests for the seven endpoints incl. non-admin 401, nil
  directory 503, error mapping.
- `plugin/installer`: moved fetcher tests unchanged.
- Manual on goblog.live: submit hello via the public form as a visitor →
  pending in admin → approve → `/plugins` and `index.json` show it → install
  from jasonernst.com Admin → Plugins.

## PRs

1. goblog — `plugins/directory/registry` port, DB-backed directory,
   submission page, fetcher move, contract doc.
2. goblog — admin API + Directory tab (`themes/default`).
3. goblog-site-theme — `admin_plugins.html`.
4. plugins — retirement.
5. iac — Renovate bump after `v0.4.0`.

## Out of scope / accepted risks

- Notifying submitters (email / status URL).
- Proof of repo ownership by the submitter.
- Install counts, telemetry, theme directory (#560).
- Docker second fence for validation.
- No cap on the number of pending submissions outstanding at once — 5/h/IP
  is the only bound.
- A hostile module can hold the validation lock for up to the full 90 s
  submit budget (the Docker fence that would have contained this was
  deliberately dropped, see above).
- The admin API's CSRF exposure (#571) now also covers this flow: submit
  publicly, then CSRF an admin into hitting `/approve` on it. Hardening is
  tracked there, not here.
