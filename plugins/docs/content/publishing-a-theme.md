# Publishing a theme

The [directory](/docs#the-directory) at `/themes` lists themes that were submitted at [`/themes/submit`](/themes/submit) and approved by a maintainer. A theme is a GitHub repository; each `vX.Y.Z` release is a version. When you submit, the directory checks the repository on the spot — the checks are listed under [Submit](#submit) — and, if it passes, queues it for a maintainer to review. Once approved it appears in the listing within minutes, and the directory re-checks it on a schedule and picks up new releases by itself.

You do not say whether you are submitting a theme or a plugin: the directory tells them apart by which manifest the latest release carries, `goblog-theme.json` or `goblog-plugin.json`. A repository must have exactly one of the two. Either submit form accepts either kind; the confirmation says which one it was filed as.

There is no build step and no release workflow. The directory downloads GitHub's archive of the tag, keeps only `templates/` and `static/`, and parses the templates the way goblog will. How to write the templates themselves is covered in [Writing a theme](/docs/writing-a-theme); this page is about what the repository must look like to be listed.

## What the repository must contain

At the root of the repository, at the release tag being published (submission checks the **latest** release):

| File | Required | Notes |
|---|---|---|
| `goblog-theme.json` | yes | the manifest, below |
| `templates/*.html` | yes (at least 1) | only the templates you change; goblog loads them on top of its `default` theme, so everything you don't ship renders from default. Each file must be **256 KiB or smaller**. Only files directly under `templates/` are loaded as templates; files in subdirectories are still extracted, counted, hashed and installed, but never parsed. |
| `static/` | no | CSS and images, served at `/theme/…`; files you don't ship fall back to default's |
| `README.md` | yes | shown on the theme's directory page; its rendered HTML must be 1 MiB or smaller |
| `screenshot.png` or `screenshot.jpg` | yes | **1 MiB or smaller**, shown in the listing, hot-linked from the tag on GitHub. `.png` is looked for first. |
| `CHANGELOG.md` | no | shown when present; same 1 MiB limit on the rendered HTML |
| `LICENSE` | recommended | not checked by the validator; state the same license as `license` in the manifest |

The archive of the tag must be **16 MiB or smaller**, with **at most 2000 entries**, **no symlinks**, and no entry whose extracted size — alone or in total — exceeds 16 MiB. Only `templates/` and `static/` are extracted and installed; everything else in the repository (the manifest, README, screenshot, your tooling) is read individually and never copied to a site. Paths with a backslash, a NUL, a leading `/` or `..` are refused, and so is a path that appears twice in the archive.

## The manifest

```json
{
  "name": "ocean",
  "display_name": "Ocean",
  "description": "One sentence shown in the listing.",
  "author": "Your Name",
  "license": "MIT",
  "min_goblog_version": "0.5.0",
  "homepage": "https://example.com/optional"
}
```

| Field | Rule |
|---|---|
| `name` | `^[a-z0-9-]+$` and unique among the directory's themes. It becomes the directory name under `themes/installed/` and the value of the site's `theme` setting once installed, so keep it stable across versions. **Reserved:** `default`, `minimal`, `installed` and `shared` are refused, and so are `submit` and `index` — the directory's own pages (`/themes/submit`, `/themes/index.json`). A goblog also never installs a directory theme over a built-in of the same name, or over a theme an operator copied in by hand. |
| `display_name` | The label in the directory listing. Required. |
| `description` | One sentence for the listing. Required. |
| `author` | Required. |
| `license` | One of exactly these SPDX identifiers: `MIT`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `MPL-2.0`, `Unlicense`, `0BSD`, `GPL-2.0-only`, `GPL-2.0-or-later`, `GPL-3.0-only`, `GPL-3.0-or-later`, `LGPL-2.1-only`, `LGPL-2.1-or-later`, `LGPL-3.0-only`, `LGPL-3.0-or-later`, `AGPL-3.0-only`, `AGPL-3.0-or-later`. Same list as for plugins; open an issue on goblog to add another. |
| `min_goblog_version` | Plain semver, three numbers, no `v` and no suffix — and **at least `0.5.0`**, the first goblog that layers themes on default and can install one from the directory. Unlike the plugin manifest, the floor is enforced. |
| `homepage` | Optional. |

`display_name`, `description` and `author` must be non-blank; whitespace alone does not count.

### Templates

Start from `themes/default/templates` in the goblog repository at the version you target and copy only the files you want to change. Templates are Go `html/template`; the `rawHTML` function and everything in `templates/shared` are available. Each file must parse on its own, on top of the shared and default sets — the directory checks that, in file-name order, and reports the first failure.

Validation parses each file; a reference to a template that does not exist (`{{ template "nope" . }}`) is only caught when the page renders, so test your theme locally before you tag. An empty override file does not blank the default template — Go keeps the earlier definition when a later one is empty; to remove a section, ship a file with content.

## Releases

- Tag releases `vX.Y.Z` — exactly three numbers, nothing else. Drafts and pre-releases are ignored.
- The directory picks the newest published, non-prerelease release by publish date and checks its tag first, so a badly-tagged latest release is an error rather than silently skipped.
- The GitHub release body is shown as the version's release notes.
- The directory lists the **latest** release; the theme's detail page shows all of its `vX.Y.Z` releases.
- Nothing has to be attached to the release. `download_url` in the index is GitHub's zip of the tag (`…/archive/refs/tags/vX.Y.Z.zip`).
- The index's `sha256` is a **content hash**, not a hash of the zip: each extracted `templates/` and `static/` file contributes its path, its length and its bytes, in path order. GitHub does not promise that a tag's zipball is byte-stable, so hashing the archive would break installs whenever GitHub re-compressed it. goblog's installer downloads the zip, extracts the same two folders, recomputes the hash and refuses the install if it differs from the index.

## Submit

Paste your repository — `owner/name`, or any `github.com/owner/name` URL — into the form at [`/themes/submit`](/themes/submit). The form is plain HTML and works without JavaScript; the result is rendered into the same page. Nothing is stored unless validation passes.

Before validation starts, the directory refuses:

- text that is not a GitHub repository (`enter a GitHub repository URL like https://github.com/owner/repo, or just owner/repo`);
- a repository that is already listed (`this repository is already listed in the directory`, with a link to its page) or already waiting (`this repository is already under review`). A rejected repository may be submitted again;
- more than **5 submissions per hour from one address** (`too many submissions from your address; try again in an hour`, HTTP 429). Refused attempts are not counted, so retrying does not push you further out;
- a submission while another one is being checked (`another submission is being checked; try again in a minute`, HTTP 429). Public validations run one at a time;
- a submission while 100 are already waiting for review (`the review queue is full; try again in a few days`). The queue is bounded as a whole, not only per address.

Validation then runs synchronously, with a 90 second budget end to end, in this order:

1. **Kind.** Fetch the latest published, non-prerelease release; its tag must be `vX.Y.Z`. Look for `goblog-plugin.json` and `goblog-theme.json` at that tag: exactly one must exist.
2. **Manifest.** Parse `goblog-theme.json` against the rules above.
3. **README.** `README.md` must exist at the tag.
4. **Screenshot.** `screenshot.png`, then `screenshot.jpg`, must exist at the tag and be 1 MiB or smaller.
5. **Archive.** GitHub's zip of the tag is downloaded (16 MiB or smaller) and unpacked in memory under the limits above, keeping `templates/` and `static/`.
6. **Templates.** There must be at least one `templates/*.html`; each is checked against the 256 KiB cap and parsed on top of goblog's shared and default templates. Every `{{ template "…" }}` they reference must then exist somewhere in that set — in the theme, in its own `{{ define }}` blocks, or in goblog's shared and default templates (a theme may reference `header.html`, `footer.html` or `admin_nav.html` without shipping them; default's copy is used).
7. **Docs.** `README.md`, `CHANGELOG.md` (if present) and every release body are rendered through GitHub's markdown API; the README, the changelog and each release's notes must each render to 1 MiB or smaller. The repository's star count is fetched too — best effort, `0` if GitHub cannot be reached for it.
8. **Name.** The theme's `name` must not already be published by a different repository.

A failure is shown above the form with the reason — the texts are in [Common validation errors](#common-validation-errors). A pass shows **Queued for review as a theme** and stores the built entry under **Pending review**. A maintainer sees the card in **Admin → Plugins → Directory**: the screenshot, the kind, display name, `name` and version, author, license and `min_goblog_version`, a link to the repository, and a **Show README** button that expands the rendered README. They approve or reject from there; approval usually takes a few days. An approved entry is in the index within minutes.

### What gets published

The index entry is built from the manifest and the latest release: `version` is the tag without `v`, `download_url` is the tag's zip, `sha256` is the content hash, `screenshot_url` is the raw GitHub URL of the screenshot at the tag, `released_at` is the release's publish time, and `stars` is the repository's GitHub star count, which the listing sorts by. The entry carries `install_type: "theme"`, `kind: "theme"` and an empty `allowed_hosts`. The detail document at `/themes/<name>.json` adds the rendered README, changelog and release history. The exact shapes are in [Directory formats](/docs/directory-formats).

### Updates

The directory's `refresh-directory` job re-checks every approved repository once the last refresh is older than the site's `refresh_minutes` setting (default 360, minimum 15). If the latest `vX.Y.Z` release is the one already listed, only the star count is refreshed. If there is a newer release, the whole validation above runs again against it and the listing is replaced. **A failed rebuild keeps the old listing**: the error is recorded on the entry and shown to the maintainer as "failed" next to "serving vX.Y.Z", but a broken release never takes a theme off the directory. Fix the release (or publish a new one) and the next refresh picks it up; a maintainer can also press **Rebuild** to re-check at once.

A theme is [code](/docs#trust-model): once an operator activates it, its templates render every page of their site, including the admin, with no sandbox. The directory checks that a theme is well-formed, not that it is benign; maintainers may decline or delist entries.

## Common validation errors

Messages are prefixed with the repository (`owner/name: …`) and, once a release has been picked, its tag too (`owner/name@v1.2.0: …`); the release-level errors (`no published release`, `release tag … must be vX.Y.Z`) carry the repository only. Manifest problems are joined with `;` after `goblog-theme.json:`; archive problems start with `archive:`; template problems start with `templates do not load:`.

| Error | What to do |
|---|---|
| `no published release (drafts and pre-releases are ignored)` | Publish a release. Drafts and pre-releases do not count. |
| `release tag "…" must be vX.Y.Z` | Re-tag the newest release as `v` plus three numbers (`v1.2.0`, not `1.2.0` or `v1.2`). |
| `has both goblog-plugin.json and goblog-theme.json; a repository is one or the other` | Remove one manifest. A theme repository has `goblog-theme.json` only. |
| `no goblog-plugin.json or goblog-theme.json at the root; see the publishing docs` | Add `goblog-theme.json` at the repository root and make sure it is in the tagged commit. |
| `goblog-theme.json: not found` | The file is missing at the tag that was checked (the latest release), even if it exists on `main`. Tag a release that includes it. |
| `goblog-theme.json: invalid character …` (or another JSON error) | The manifest is not valid JSON. |
| `name must match ^[a-z0-9-]+$` | Lower-case letters, digits and hyphens only. |
| `name "default" is reserved` (or `minimal`, `installed`, `shared`, `submit`, `index`) | Pick another name. |
| `display_name is required` / `description is required` / `author is required` | Fill in the field; whitespace alone does not count. |
| `license "…" is not a known SPDX identifier` | Use one of the identifiers listed above, spelled exactly. |
| `min_goblog_version must be a plain semver like 0.5.0` | Three numbers, no `v`, no `-beta`. |
| `min_goblog_version must be at least 0.5.0` | Directory themes need goblog 0.5.0 or newer; say so. |
| `README.md: not found` | Add a `README.md` at the repository root at the tag. |
| `screenshot.png (or screenshot.jpg) is required` | Add one at the repository root, at the tag. |
| `screenshot.png is … bytes; the limit is 1048576 (1 MiB)` | Shrink or re-encode it. GitHub's contents API cannot even return a larger file inline. |
| `archive of owner/name@v1.2.0 exceeds 16777216 bytes` | The zip of the tag is over 16 MiB. Take large files out of the repository (or out of the tagged commit). |
| `archive has … entries; the limit is 2000` | Too many files in the tag; a theme is a few dozen. Drop vendored or generated trees. |
| `archive: … is a symlink` | Replace the symlink with the file it points to. |
| `archive: unsafe path "…"` | A path with `..`, a leading `/`, a backslash or a NUL. Rename it. |
| `archive: duplicate entry "…"` | The same `templates/` or `static/` path appears twice in the zip. Rebuild the archive from a clean checkout. |
| `archive: …/templates/… is … bytes; the limit is 262144 (256 KiB)` | Split or shrink the template. goblog's largest real template is under 32 KiB. |
| `archive: extracted size exceeds 16777216 bytes` | `templates/` and `static/` together unpack to more than 16 MiB. Move large assets out of `static/`. |
| `templates do not load: no templates/*.html files: a theme must ship at least one template` | Add at least one `.html` file directly under `templates/`. |
| `templates do not load: templates/home.html: template: home.html:12: …` | A Go template parse error at that file and line. Fix it and re-tag; the same check runs when a site installs the theme. |
| `templates do not load: templates/home.html: template "home.html" references template "…", which neither the theme nor goblog's shared and default templates define` | A `{{ template "…" }}` names something that exists nowhere; the page would 500 at render. Ship the template, define it, or fix the name. |
| `rendered README.md is … bytes; the limit is 1048576 (1 MiB)` (or `CHANGELOG.md`) | Shrink the file — usually an embedded base64 image. Link images instead. |
| `rendered release notes for v1.2.0 are … bytes; the limit is 1048576 (1 MiB)` | The same cap on one release's body. Edit the release on GitHub. |
| `a different repository already publishes an entry with this name` | Another listed repository already owns that `name`. Pick another, or, if it is yours, ask a maintainer to delist the old one. |

If the message is `Something went wrong on our side; please try again later.`, the failure was on the directory (GitHub unreachable, a database error) and is logged there — not something in your repository. Try again later.
