# Plugin Install Follow-ups (registry stars + submissions, site theme, iac) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish #553 outside the goblog repo: `stars` in the registry index, the issue-driven submission workflow, the admin Plugins template in goblog.live's theme, and the deployment prerequisites for installing plugins on goblog.live.

**Architecture:** Registry (`goblogplatform/plugins`): `Source` gains `RepoInfo` (stargazers), `Build` writes `stars`; an issue form + `submit.yml` workflow validates a submitted repo in a read-only job (via `registry build` into a temp dir, which also checks name uniqueness) and, on success, a second job with write permissions opens the `registry.yaml` PR. Site theme: copy `admin_plugins.html` and the nav link. iac: env var + persisted bind mount for goblog.live.

**Tech Stack:** Go (go-github v92), GitHub Actions + issue forms, `gh` CLI, Ansible.

**Spec:** `docs/superpowers/specs/2026-09-17-plugin-install-admin-design.md` §4 (registry half) and §5.

## Global Constraints

- Every repo: branch + PR, never push to `main`, never merge; commit messages imperative with the trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>` (goblog-side commits also get the `(#553)` suffix). `git branch --show-current` before every commit.
- Registry: no new deps; `stars` json field name exactly `stars` (goblog's `directory.Entry` reads it); Go toolchain quirk on this machine: prefix `go` commands with `GOTOOLCHAIN=go1.26.1` if `go` complains; snap Docker: set `TMPDIR=$HOME/.cache/goblog-registry` for anything that runs the validator.
- Submission workflow: the job that executes the submitted plugin (inside the Docker sandbox) must have `contents: read` only; only a separate job that does not touch the plugin gets write permissions. PRs opened with `GITHUB_TOKEN` do **not** trigger the `Validate` workflow (GitHub rule) — the workflow uses `secrets.SUBMIT_TOKEN` when present, else `GITHUB_TOKEN`, and documents the difference.
- Local checkouts: `~/dev/goblog-plugins`, `~/dev/goblog-site-theme` (clone if missing), `~/dev/iac`.

---

### Task 1: Registry — `stars` in the index

**Files (in `~/dev/goblog-plugins`, branch `feat/stars`):**
- Modify: `internal/registry/source.go` (interface + `GitHubSource`), `internal/registry/build.go` (`IndexEntry.Stars`, `buildDetail`), `internal/registry/source_test.go` (fake endpoint), `internal/registry/validate_test.go` and `cmd/registry/main_test.go` (`memSource.RepoInfo`), `internal/registry/build_test.go`, `README.md` (one line).

**Interfaces:**
- Produces: `Source.RepoInfo(ctx, owner, repo string) (stars int, err error)`; `IndexEntry.Stars int \`json:"stars"\`` (after `DetailURL`).

- [ ] **Step 1: Failing tests**

`internal/registry/source_test.go` — add to `fakeGitHub` a handler:
```go
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"full_name":"o/r","stargazers_count":42}`))
	})
```
and to `TestGitHubSource`, after the `File` checks:
```go
	stars, err := src.RepoInfo(ctx, "o", "r")
	if err != nil || stars != 42 {
		t.Errorf("RepoInfo = %d, %v", stars, err)
	}
	if _, err := src.RepoInfo(ctx, "o", "missing"); err == nil {
		t.Error("RepoInfo on an unknown repo should fail")
	}
```
`internal/registry/validate_test.go` `memSource`: add `stars map[string]int` and
```go
func (m *memSource) RepoInfo(_ context.Context, owner, repo string) (int, error) {
	if n, ok := m.stars[owner+"/"+repo]; ok {
		return n, nil
	}
	return 0, nil
}
```
and in `helloSource()` set `stars: map[string]int{"o/hello": 7}`. `cmd/registry/main_test.go` `memSource`: add `func (m *memSource) RepoInfo(context.Context, string, string) (int, error) { return 3, nil }`.
`internal/registry/build_test.go` `TestBuild_WritesIndexAndDetails`: add `Stars: 7,` to the `want := IndexEntry{...}` literal, and after the detail assertions `if d.Stars != 7 { t.Errorf("detail stars = %d", d.Stars) }`.

Run: `GOTOOLCHAIN=go1.26.1 go test ./... 2>&1 | head` → FAIL to compile (`memSource` missing method / `Stars` unknown field).

- [ ] **Step 2: Implement**

`source.go` — interface gets:
```go
	// RepoInfo returns the repository's GitHub stargazer count (the
	// directory's "top plugins" ordering).
	RepoInfo(ctx context.Context, owner, repo string) (stars int, err error)
```
and:
```go
func (g *GitHubSource) RepoInfo(ctx context.Context, owner, repo string) (int, error) {
	r, _, err := g.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}
	return r.GetStargazersCount(), nil
}
```
(`go doc github.com/google/go-github/v92/github Repository.GetStargazersCount` to confirm the accessor; if v92 exposes `StargazersCount int` directly, use that.)
`build.go` — `IndexEntry` gets `Stars int \`json:"stars"\`` after `DetailURL`; in `buildDetail`, after `entry := IndexEntry{...}`:
```go
	stars, err := src.RepoInfo(ctx, v.Owner, v.Name)
	if err != nil {
		return DetailDoc{}, err
	}
	entry.Stars = stars
```
`README.md` — in the bullet describing `index.json`, add "and `stars` (GitHub stargazers, the directory's default ordering)".

- [ ] **Step 3: Verify, commit, PR**

`gofmt -l . && GOTOOLCHAIN=go1.26.1 go vet ./... && GOTOOLCHAIN=go1.26.1 go test ./...` → PASS. Real check: `TMPDIR=$HOME/.cache/goblog-registry GITHUB_TOKEN=$(gh auth token) GOTOOLCHAIN=go1.26.1 go run ./cmd/registry build --out /tmp/dist-stars` → `dist/index.json` has `"stars": <n>` for hello (n ≥ 0).
```bash
git checkout -b feat/stars main && git add -A && git commit -m "Add GitHub stars to the index

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>" && git push -u origin feat/stars
gh pr create --base main --title "Add GitHub stars to the index" --body "Adds \`stars\` (stargazers_count) to every index entry and detail doc; goblog's directory page and Admin → Plugins sort by it (goblogplatform/goblog#570).

🤖 Generated with [Claude Code](https://claude.com/claude-code)"
gh pr checks --watch
```
Expected: `Validate` passes.

---

### Task 2: Registry — submission issue form and workflow

**Files (in `~/dev/goblog-plugins`, branch `feat/submissions` off `feat/stars` or `main`):**
- Create: `.github/ISSUE_TEMPLATE/submit-plugin.yml`, `.github/ISSUE_TEMPLATE/config.yml`, `.github/workflows/submit.yml`
- Modify: `docs/CONTRACT.md` ("Submit" section), `README.md`

- [ ] **Step 1: Issue form**

`.github/ISSUE_TEMPLATE/submit-plugin.yml`:
```yaml
name: Submit a plugin
description: Add your goblog plugin repository to the directory
title: "Submit: "
labels: [submission]
body:
  - type: markdown
    attributes:
      value: |
        Your repository must follow the [contract](https://github.com/goblogplatform/plugins/blob/main/docs/CONTRACT.md): `goblog-plugin.json`, `README.md`, the plugin `.go` file, and a release tagged `vX.Y.Z`. A workflow validates it and, if it passes, opens the pull request for you.
  - type: input
    id: repo
    attributes:
      label: Repository
      description: GitHub repository as owner/name
      placeholder: goblogplatform/goblog-plugin-hello
    validations:
      required: true
  - type: checkboxes
    id: contract
    attributes:
      label: Contract
      options:
        - label: My repository has goblog-plugin.json, README.md, the plugin file, and a published vX.Y.Z release
          required: true
```
`.github/ISSUE_TEMPLATE/config.yml`:
```yaml
blank_issues_enabled: true
```

- [ ] **Step 2: Workflow**

`.github/workflows/submit.yml`:
```yaml
name: Submission
on:
  issues:
    types: [opened, edited]
permissions: {}
jobs:
  check:
    # Only issues from the "Submit a plugin" form carry this label.
    if: contains(github.event.issue.labels.*.name, 'submission')
    runs-on: ubuntu-latest
    permissions:
      contents: read
    outputs:
      repo: ${{ steps.parse.outputs.repo }}
      ok: ${{ steps.validate.outputs.ok }}
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Parse the repository from the issue form
        id: parse
        env:
          BODY: ${{ github.event.issue.body }}
        run: |
          repo=$(printf '%s\n' "$BODY" | awk '/^### Repository/{f=1; next} f && NF {print; exit}' | tr -d '[:space:]')
          repo=${repo#https://github.com/}
          repo=${repo%.git}
          if ! printf '%s' "$repo" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'; then
            echo "::error::Could not read an owner/name repository from the issue form"
            exit 1
          fi
          echo "repo=$repo" >> "$GITHUB_OUTPUT"
      - name: Validate the submission (runs the plugin in the sandbox)
        id: validate
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          REPO: ${{ steps.parse.outputs.repo }}
        run: |
          if grep -Fxq "  - repo: $REPO" registry.yaml; then
            echo "already listed" > result.txt
            echo "ok=false" >> "$GITHUB_OUTPUT"
            exit 0
          fi
          printf '  - repo: %s\n' "$REPO" >> registry.yaml
          go build -o "$RUNNER_TEMP/registry" ./cmd/registry
          set +e
          "$RUNNER_TEMP/registry" build --out "$RUNNER_TEMP/dist" --image compscidr/goblog:v0.2.7 > result.txt 2>&1
          set -e
          cat result.txt
          if grep -Fxq "$REPO: built" result.txt; then echo "ok=true" >> "$GITHUB_OUTPUT"; else echo "ok=false" >> "$GITHUB_OUTPUT"; fi
      - uses: actions/upload-artifact@v7
        if: always()
        with:
          name: result
          path: result.txt
  respond:
    needs: check
    if: always() && needs.check.result != 'skipped'
    runs-on: ubuntu-latest
    permissions:
      contents: write
      pull-requests: write
      issues: write
    env:
      GH_TOKEN: ${{ secrets.SUBMIT_TOKEN || secrets.GITHUB_TOKEN }}
      REPO: ${{ needs.check.outputs.repo }}
      ISSUE: ${{ github.event.issue.number }}
    steps:
      - uses: actions/checkout@v7
        with:
          token: ${{ secrets.SUBMIT_TOKEN || secrets.GITHUB_TOKEN }}
      - uses: actions/download-artifact@v8
        continue-on-error: true
        with:
          name: result
      - name: Open the registry pull request
        if: needs.check.outputs.ok == 'true'
        run: |
          branch="submit/$(printf '%s' "$REPO" | tr '/' '-')"
          if gh pr list --head "$branch" --state open --json url --jq '.[0].url' | grep -q .; then
            url=$(gh pr list --head "$branch" --state open --json url --jq '.[0].url')
            gh issue comment "$ISSUE" --body "A pull request for \`$REPO\` is already open: $url"
            exit 0
          fi
          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git checkout -b "$branch"
          printf '  - repo: %s\n' "$REPO" >> registry.yaml
          git add registry.yaml
          git commit -m "Add $REPO"
          git push -u origin "$branch"
          url=$(gh pr create --base main --head "$branch" --title "Add $REPO" --body "Submitted in #$ISSUE. Validation passed in the submission workflow (run ${{ github.run_id }}).

Closes #$ISSUE")
          gh issue comment "$ISSUE" --body "Validation passed — opened $url for a maintainer to merge. Thanks!"
      - name: Report a failed validation
        if: needs.check.outputs.ok != 'true'
        run: |
          if [ -f result.txt ]; then out=$(head -c 6000 result.txt); else out="the workflow could not read a repository from the form (see run ${{ github.run_id }})"; fi
          if [ "$out" = "already listed" ]; then
            gh issue comment "$ISSUE" --body "\`$REPO\` is already listed in the registry."
          else
            gh issue comment "$ISSUE" --body "Validation of \`$REPO\` failed. Fix the repository (see [docs/CONTRACT.md](https://github.com/goblogplatform/plugins/blob/main/docs/CONTRACT.md)), then edit this issue to re-run.

\`\`\`
$out
\`\`\`"
          fi
```
Action majors above are current as of 2026-09-17 (`checkout@v7`, `setup-go@v7`, `upload-artifact@v7`, `download-artifact@v8`); re-check with `gh api repos/actions/<name>/releases/latest --jq .tag_name` if this plan is executed later.

- [ ] **Step 3: Docs + repo setting**

`docs/CONTRACT.md` "## Submit" — replace the fork/PR steps with: "1. Open a [submission issue](https://github.com/goblogplatform/plugins/issues/new?template=submit-plugin.yml) with your `owner/name` (the box on goblog.live/plugins does this for you). 2. The `Submission` workflow validates the repository and comments the result; if it passes it opens the `registry.yaml` pull request. 3. A maintainer merges it; the index rebuilds within minutes." Keep the PR-by-hand route as an alternative sentence.
`README.md` — add a "Submissions" paragraph describing the two jobs and: "Set a repository secret `SUBMIT_TOKEN` (fine-grained PAT with Contents, Pull requests and Issues write on this repo) so the bot's pull requests trigger the `Validate` check; with the default `GITHUB_TOKEN` they don't (GitHub prevents token-created PRs from starting workflows)."
Repo setting: `gh api -X PUT repos/goblogplatform/plugins/actions/permissions/workflow -f default_workflow_permissions=read -F can_approve_pull_request_reviews=true` (this is the "Allow GitHub Actions to create and approve pull requests" toggle).

- [ ] **Step 4: Commit, PR, prove**

```bash
git checkout -b feat/submissions main && git add -A && git commit -m "Add the plugin submission issue form and workflow

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>" && git push -u origin feat/submissions
gh pr create --base main --title "Add the plugin submission issue form and workflow" --body "..." ; gh pr checks --watch
```
`issues` events run the workflow from the **default branch**, so the live proof happens after the maintainer merges: then open two issues via the form URL — `goblogplatform/goblog-plugin-hello` (expect the "already listed" comment) and `goblogplatform/goblog` (expect a failure comment quoting `goblog-plugin.json … not found`); close both. Record in the report that the success path (PR creation) is verified by reading only until a new plugin repo submits.

---

### Task 3: goblog-site-theme — admin Plugins template

**Files (in `~/dev/goblog-site-theme`, branch `feat/admin-plugins`):** copy `templates/admin_plugins.html` from goblog `themes/default/templates/`; add `<a class="nav-link" href="/admin/plugins">Plugins</a>` between Post Types and Settings in `templates/admin_nav.html`. Also copy `admin_users.html` and `admin_comments.html` from goblog's default theme (the site theme lacks them, so `/admin/users` and `/admin/comments` currently 500 on goblog.live) — say so in the PR as a bonus fix.

- [ ] Clone if needed: `gh repo clone goblogplatform/goblog-site-theme ~/dev/goblog-site-theme`; branch; copy; `git diff --stat`; commit; push; PR titled "Add the admin Plugins page (and the missing Users/Comments admin templates)".
- [ ] Sanity: `grep -c '{{ template "header.html"' templates/admin_plugins.html` = 1; the theme's `header.html` must provide what the template uses (Bootstrap JS for tabs — check `templates/_head.html`/`header.html` in the site theme loads `bootstrap.min.js` for `admin_page`; if not, note it in the PR).

---

### Task 4: iac — enable dynamic plugins on goblog.live

**Files (in `~/dev/iac`, branch `feat/goblog-live-dynamic-plugins`):** `ansible/roles/projects/tasks/main.yml`.

- [ ] In "Create goblog.live directories" add `- plugins-dynamic` to the loop. In "Copy goblog.live .env" append `ENABLE_DYNAMIC_PLUGINS=true` to the content (goblog loads `.env` into the environment before reading it). In "Deploy goblog.live" add the volume `- /opt/goblog-live/plugins-dynamic:/go/src/github.com/compscidr/goblog/plugins/dynamic`. Do not touch `jasonernst_com` (optional, separate decision).
- [ ] `ansible-lint ansible/roles/projects/tasks/main.yml` if available, else `python3 -c 'import yaml;yaml.safe_load(open("ansible/roles/projects/tasks/main.yml"))'`; commit; push; PR "Enable dynamic plugin installs on goblog.live" whose body says: requires goblog ≥ v0.2.8 (contains goblogplatform/goblog#570) and that the directory is persisted so installs survive redeploys.

---

### Task 5: goblog docs PR + hand-off

- [ ] In goblog (branch `docs/553-followups`, this plan): push, PR "Docs: plugin install follow-ups plan (#553)".
- [ ] Report the human steps: merge the three PRs; `gh release create v0.2.8 --generate-notes` in goblog; merge Renovate's iac bump (or edit the tag) and run `ansible-playbook -i inventory.yml projects.yml --limit projects --tags goblog-live`; then on goblog.live: Admin → Plugins shows the Browse tab with `hello` and Install works; optionally add `SUBMIT_TOKEN` to the registry repo.
