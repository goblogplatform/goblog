# Publishing a plugin

The [directory](/docs#the-directory) at `/plugins` lists plugins that were submitted at [`/plugins/submit`](/plugins/submit) and approved by a maintainer. A plugin is a GitHub repository whose releases each carry a compiled WebAssembly module; each GitHub release is a version. When you submit, the directory checks the repository on the spot — the checks are listed under [Submit](#submit) — and, if it passes, queues it for a maintainer to review. Once approved it appears in the listing within minutes, and the directory re-checks it on a schedule and picks up new releases by itself.

You do not say whether you are submitting a plugin or a theme: the directory tells them apart by which manifest the latest release carries, `goblog-plugin.json` or `goblog-theme.json`. A repository must have exactly one of the two.

**Only WebAssembly plugins are accepted.** Yaegi-interpreted `.go` plugins (goblog's [dynamic plugins](https://github.com/goblogplatform/goblog#dynamic-plugins)) still work when an operator drops the file in by hand, but the directory does not list or install them: a `.wasm` module can bundle any dependency its author likes, runs with no filesystem and no network beyond the hosts it declares, and needs no goblog rebuild.

## What the repository must contain

At the root of the repository, at the release tag being published (submission checks the **latest** release):

| File | Required | Notes |
|---|---|---|
| `goblog-plugin.json` | yes | the manifest — field by field in [Writing a plugin](/docs/writing-a-plugin#the-manifest) |
| the plugin's source | yes | anything that builds the module — Go with [`github.com/extism/go-pdk`](https://github.com/extism/go-pdk), TinyGo, Rust, or any language with an [Extism PDK](https://extism.org/docs/concepts/pdk) |
| `README.md` | yes | shown on the plugin's directory page; its rendered HTML must be 1 MiB or smaller |
| `CHANGELOG.md` | no | shown when present; same 1 MiB limit on the rendered HTML |
| `LICENSE` | recommended | not checked by the validator; state the same license as `license` in the manifest |

And attached to every release: the compiled module, named as `entry` in the manifest (`plugin.wasm` unless you set it). The module is a release **asset**, not a file in the repository — the directory validates and publishes the asset and never looks for a `.wasm` file in the tree.

### Manifest rules that matter for publishing

The full field table is on the [Writing a plugin](/docs/writing-a-plugin#the-manifest) page. What the directory enforces when it parses `goblog-plugin.json`:

- `name` must match `^[a-z0-9-]+$` and equal the `name` your `identity` export returns. It has to be unique among the directory's plugins: if a *different* repository already publishes an entry with that name, your submission is refused, even after it validates.
- `display_name`, `description` and `author` must be non-blank. `display_name` is the label in the directory listing; it does not have to equal the `display_name` your `identity` export returns, which titles the plugin's settings page in the admin.
- `license` must be one of exactly these SPDX identifiers: `MIT`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `MPL-2.0`, `Unlicense`, `0BSD`, `GPL-2.0-only`, `GPL-2.0-or-later`, `GPL-3.0-only`, `GPL-3.0-or-later`, `LGPL-2.1-only`, `LGPL-2.1-or-later`, `LGPL-3.0-only`, `LGPL-3.0-or-later`, `AGPL-3.0-only`, `AGPL-3.0-or-later`. The list is short on purpose; open an issue on goblog to add another.
- `runtime` must be `"wasm"`.
- `entry` defaults to `plugin.wasm`; otherwise letters, digits, `_`, `.` and `-` only, ending in `.wasm`, with no path.
- `allowed_hosts` entries are hostnames, IPs or globs (`*.example.com`), each optionally with a port, never a scheme or a path. A glob must still name a domain: `*` alone, `**` or `*.*` is rejected. Omitted or empty means no network at all. The directory publishes this list and shows it to maintainers before approval and to operators before they install, so declare only what you use.
- `min_goblog_version` must be plain semver, three numbers, no `v` and no suffix. WebAssembly plugins need at least `0.2.9`; the directory checks the format, not the floor.

## Releases

- Tag releases `vX.Y.Z` — exactly three numbers, nothing else. Drafts and pre-releases are ignored.
- The tag without `v` must equal the `version` your `identity` export returns. The directory picks the newest published, non-prerelease release by publish date and checks its tag first, so a badly-tagged latest release is an error rather than silently skipped.
- **Every release must have the module attached** as the asset named by `entry`. A release without it fails validation with `has no asset named plugin.wasm`.
- The asset must be 16 MiB or smaller.
- The GitHub release body is shown as the version's release notes.
- The directory lists the **latest** release; the plugin's detail page shows all of its `vX.Y.Z` releases.

Copy this workflow — it is [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello)'s `.github/workflows/release.yml`, verbatim — and the asset is built and uploaded whenever you publish a release:

```yaml
name: Release
on:
  release:
    types: [published]
permissions:
  contents: write
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - name: Build plugin.wasm
        run: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -ldflags="-s -w" -o plugin.wasm .
      - name: Upload to the release
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release upload "${{ github.event.release.tag_name }}" plugin.wasm --clobber
```

The directory reads the asset at the moment it validates. If the workflow is still uploading when you submit, the submission fails with the `has no asset` error; submit again once the asset is on the release.

Before you tag, run the same load check the directory will: [`validate-plugin`](/docs/writing-a-plugin#validate) on your built module. It has to print an identity whose `name` matches the manifest and whose `version` matches the tag you are about to create.

## Submit

Paste your repository — `owner/name`, or any `github.com/owner/name` URL — into the form at [`/plugins/submit`](/plugins/submit). The form is plain HTML and works without JavaScript; the result is rendered into the same page. Nothing is stored unless validation passes.

Before validation starts, the directory refuses:

- text that is not a GitHub repository (`enter a GitHub repository URL like https://github.com/owner/repo, or just owner/repo`);
- a repository that is already listed (`this repository is already listed in the directory`, with a link to its page) or already waiting (`this repository is already under review`). A rejected repository may be submitted again;
- more than **5 submissions per hour from one address** (`too many submissions from your address; try again in an hour`, HTTP 429). Refused attempts are not counted, so retrying does not push you further out;
- a submission while another one is being checked (`another submission is being checked; try again in a minute`, HTTP 429). Public validations run one at a time.

Validation then runs synchronously, with a 90 second budget end to end, in this order:

1. **Kind.** Fetch the latest published, non-prerelease release; its tag must be `vX.Y.Z`. Look for `goblog-plugin.json` and `goblog-theme.json` at that tag: exactly one must exist.
2. **Manifest.** Parse `goblog-plugin.json` against the rules above.
3. **README.** `README.md` must exist at the tag.
4. **Asset.** The release must carry an asset named by `entry`, 16 MiB or smaller. It is downloaded.
5. **Load.** The module is instantiated in goblog's own sandbox — no store, no allowed hosts, the runtime's 64 MB memory cap and call timeouts, and nothing cached afterwards — and its `identity`, `settings`, `pages` and `jobs` exports are read. This is the same check as `validate-plugin`. The identity must have a non-empty name and version.
6. **Match.** The identity's `name` must equal the manifest's `name`; its `version` must equal the tag without `v`.
7. **Docs.** `README.md`, `CHANGELOG.md` (if present) and every release body are rendered through GitHub's markdown API; the README and changelog HTML must each be 1 MiB or smaller. The repository's star count is fetched too — best effort, `0` if GitHub cannot be reached for it.
8. **Name.** The plugin's `name` must not already be published by a different repository.

A failure is shown above the form with the reason — the texts are in [Common validation errors](#common-validation-errors). A pass shows **Queued for review as a plugin** and stores the built entry under **Pending review**. A maintainer sees the card in **Admin → Plugins → Directory**: the kind, display name, `name` and version, author, license and `min_goblog_version`, the `allowed_hosts` list as "Talks to: …" (or "No network access", with `*`, other globs and local-network addresses flagged), a link to the repository, and a **Show README** button that expands the rendered README. They approve or reject from there; approval usually takes a few days. An approved entry is in the index within minutes.

### What gets published

The index entry for your plugin is built from the manifest, the latest release and its module asset: `download_url` is the asset's browser download URL, `sha256` is of the asset's bytes, `version` is the tag without `v`, `released_at` is the release's publish time, and `stars` is the repository's GitHub star count, which the listing sorts by. The entry also carries `runtime: "wasm"`, `install_type: "wasm"` and the manifest's `allowed_hosts` (always a list, `[]` for no network), which goblog's **Admin → Plugins** page shows as "Talks to: …" before an operator installs. The detail document at `/plugins/<name>.json` adds the rendered README, changelog and release history. The exact shapes are in [Directory formats](/docs/directory-formats).

### Updates

The directory's `refresh-directory` job re-checks every approved repository once the last refresh is older than the site's `refresh_minutes` setting (default 360, minimum 15). If the latest `vX.Y.Z` release is the one already listed, only the star count is refreshed. If there is a newer release, the whole validation above runs again against it and the listing is replaced. **A failed rebuild keeps the old listing**: the error is recorded on the entry and shown to the maintainer as "failed" next to "serving vX.Y.Z", but a broken release never takes a plugin off the directory. Fix the release (or publish a new one) and the next refresh picks it up; a maintainer can also press **Rebuild** to re-check at once.

Any goblog can host a directory (turn it on under **Admin → Plugins → Plugin Directory → Settings**), so submitting to one instance does not list you on another; goblog.live is the one most installs read from.

Plugins run inside the goblog process of whoever installs them, [sandboxed](/docs#trust-model) but trusted with the hosts they declare and the settings they are given. Keep them small and readable; the directory is curated and maintainers may decline or delist entries.

## Common validation errors

Messages are prefixed with the repository (`owner/name: …`) and, once a release has been picked, its tag too (`owner/name@v1.2.0: …`); the release-level errors (`no published release`, `release tag … must be vX.Y.Z`, `release vX.Y.Z has no asset named …`) carry the repository only. Manifest problems are joined with `;` after `goblog-plugin.json:`.

| Error | What to do |
|---|---|
| `no published release (drafts and pre-releases are ignored)` | Publish a release. Drafts and pre-releases do not count; the GitHub release must be published. |
| `release tag "…" must be vX.Y.Z` | Re-tag the newest release as `v` plus three numbers (`v1.2.0`, not `1.2.0` or `v1.2`). |
| `has both goblog-plugin.json and goblog-theme.json; a repository is one or the other` | Remove one manifest. A plugin repository has `goblog-plugin.json` only. |
| `no goblog-plugin.json or goblog-theme.json at the root; see the publishing docs` | Add `goblog-plugin.json` at the repository root and make sure it is in the tagged commit. |
| `goblog-plugin.json: not found` | The file is missing at the tag that was checked (the latest release), even if it exists on `main`. Tag a release that includes it. |
| `goblog-plugin.json: invalid character …` (or another JSON error) | The manifest is not valid JSON. |
| `name must match ^[a-z0-9-]+$` | Lower-case letters, digits and hyphens only. |
| `display_name is required` / `description is required` / `author is required` | Fill in the field; whitespace alone does not count. |
| `license "…" is not a known SPDX identifier` | Use one of the identifiers listed above, spelled exactly. |
| `runtime must be "wasm": the directory only lists WebAssembly plugins; see /docs/publishing-a-plugin` | Set `"runtime": "wasm"`. Yaegi `.go` plugins are not listed. |
| ``entry must be a .wasm release asset name (letters, digits, `_`, `.`, `-`)`` | `entry` is an asset file name ending in `.wasm`, with no directory part. |
| `allowed_hosts entries must be hostnames, IPs or globs without scheme or path` | Write `api.example.com`, not `https://api.example.com/v1`. |
| `allowed_hosts entries must name a host; "*" alone is not allowed` | Replace `*` (or `**`, `*.*`) with the hosts you actually call. |
| `min_goblog_version must be a plain semver like 0.2.6` | Three numbers, no `v`, no `-beta`. |
| `README.md: not found` | Add a `README.md` at the repository root at the tag. |
| `release v1.2.0 has no asset named plugin.wasm (the release workflow must upload it)` | Attach the module to the release — add the workflow above, or upload the asset by hand — then submit again. If you changed `entry`, the asset name must match it. |
| `asset plugin.wasm is … bytes; the limit is 16777216 (16 MiB)` | Build with `-ldflags="-s -w"`; drop large embedded data. |
| `plugin.wasm does not load: …` | The module failed to instantiate or one of `identity`, `settings`, `pages`, `jobs` returned an error. Run `validate-plugin` locally and fix what it reports. Those four exports must not depend on the store or the network. |
| `plugin.wasm does not load: wasm plugin: identity must include name and version` | `identity` must return both `name` and `version`. |
| `Name() is "…" but the manifest says "…"` | The `name` in `identity` and the `name` in `goblog-plugin.json` must be the same string. |
| `Version() is "…" but the release tag says "…"` | Bump the version in `identity` to match the tag (without `v`) — or tag a release that matches the code. |
| `rendered README.md is … bytes; the limit is 1048576 (1 MiB)` (or `CHANGELOG.md`) | Shrink the file — usually an embedded base64 image. Link images instead. |
| `a different repository already publishes an entry with this name` | Another listed repository already owns that `name`. Pick another, or, if it is yours, ask a maintainer to delist the old one. |

If the message is `Something went wrong on our side; please try again later.`, the failure was on the directory (GitHub unreachable, a database error) and is logged there — not something in your repository. Try again later.
