# Publishing a goblog theme

The directory at [goblog.live/themes](https://goblog.live/themes) lists themes submitted at [goblog.live/themes/submit](https://goblog.live/themes/submit) and approved by a maintainer. A theme is a GitHub repository; each `vX.Y.Z` tag is a version. Submission checks the repository right away and queues it; once approved, the directory re-checks it every few hours and picks up new releases on its own.

## What the repository must contain

At the root, at the release tag:

| File | Required | Notes |
|---|---|---|
| `goblog-theme.json` | yes | the manifest, below |
| `templates/*.html` | yes (≥ 1) | only the templates you change; goblog loads them on top of its `default` theme, so everything you don't ship renders from default. Each template file must be at most 256 KiB. |
| `static/` | no | CSS/images, served at `/theme/…`; files you don't ship fall back to default's |
| `README.md` | yes | shown on the theme's directory page |
| `screenshot.png` or `screenshot.jpg` | yes | ≤ 1 MiB, shown in the listing (hot-linked from the tag) |
| `CHANGELOG.md` | no | shown when present |

No build step and no release workflow: the directory downloads GitHub's archive of the tag. Only `templates/` and `static/` are installed; the archive must be ≤ 16 MiB, ≤ 2000 entries, with no symlinks.

### `goblog-theme.json`

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

- `name`: `^[a-z0-9-]+$`, unique among themes, becomes the directory name under `themes/installed/` and the value of the `theme` setting. Not `default`, `minimal`, `installed` or `shared`, and a goblog never installs a directory theme over a built-in of the same name.
- `license`: an SPDX identifier from the list in `plugins/directory/registry/manifest.go`.
- `min_goblog_version`: plain semver; themes need at least `0.5.0` (the first goblog that layers themes on default).

### Templates

Start from `themes/default/templates` in the goblog repository at the version you target and copy only the files you want to change. Templates are Go `html/template`; the `rawHTML` function and everything in `templates/shared` are available. Each file must parse on its own — the directory checks that.

Validation parses each file; a reference to a template that does not exist (`{{ template "nope" . }}`) is only caught when the page renders, so test your theme locally. An empty override file does not blank the default template — Go keeps the earlier definition when a later one is empty; to remove a section, ship a file with content.

### Releases

- Tag releases `vX.Y.Z`; drafts and pre-releases are ignored.
- The release body is shown as the version's notes.
- The index's `sha256` is a hash of the extracted `templates/` and `static/` files, not of the zip, so GitHub re-compressing an archive does not break installs.
