# Directory formats

A directory publishes two indexes, one per kind: `/plugins/index.json` and `/themes/index.json`, each a JSON array with one entry per approved repository, plus a detail document per entry at `/plugins/<name>.json` or `/themes/<name>.json`. [goblog.live](https://www.goblog.live) hosts the public ones; every goblog's **Admin → Plugins** and **Admin → Themes** read the index at its `plugin_directory_url` and `theme_directory_url` settings, which default to goblog.live's. Any goblog can host its own directory with the same shapes — see [Running a private directory](#running-a-private-directory).

The types behind these documents are `IndexEntry`, `DetailDoc` and `ReleaseDoc` in `plugins/directory/registry/build.go`; their JSON tags are the contract, and both the directory and the installers are compiled from the same definitions.

## index.json

The array is sorted by `name`; the listings sort it by `stars` before showing it. An entry describes the **latest** `vX.Y.Z` release of one repository, and it is the whole of what an installer needs: the installers never read the detail document.

| Field | Type | Plugin | Theme |
|---|---|---|---|
| `name` | string | The manifest's `name`: `^[a-z0-9-]+$`, unique per kind. Becomes the module's file name under `plugins/wasm/`. | Same rule; becomes the directory under `themes/installed/` and the `theme` setting. |
| `display_name` | string | From the manifest; shown in listings. | Same. |
| `description` | string | From the manifest. | Same. |
| `version` | string | The release tag without `v`, e.g. `2.0.0`. Must equal what the module's `identity` export reports. | The tag without `v`. |
| `author` | string | From the manifest. | Same. |
| `license` | string | The manifest's SPDX identifier. | Same. |
| `source_url` | string | `https://github.com/<owner>/<repo>`. | Same. |
| `download_url` | string | The release asset named by the manifest's `entry` (`plugin.wasm`), as GitHub's browser download URL: `…/releases/download/vX.Y.Z/plugin.wasm`. | GitHub's archive of the tag: `…/archive/refs/tags/vX.Y.Z.zip`. |
| `sha256` | string | Hex SHA-256 of the asset's bytes. | The **content hash** of the extracted `templates/` and `static/` files (below), not of the zip. |
| `min_goblog_version` | string | From the manifest; an installer refuses the entry when the running goblog is older. Development builds are treated as compatible. | Same; at least `0.5.0`. |
| `install_type` | string | `wasm`. The plugin installer only installs entries with this value. | `theme`. The theme installer only installs entries with this value. |
| `runtime` | string | `wasm`. | Empty. |
| `allowed_hosts` | array of strings | The manifest's list, never null: `[]` when the plugin makes no requests. The installer writes it next to the module and enforces it; **Admin → Plugins** shows it as "Talks to: …" before Install. | Always `[]`. |
| `released_at` | string | The release's publish time, RFC 3339 in UTC. | Same. |
| `detail_url` | string | Absolute URL of the detail document, `<site_url>/plugins/<name>.json`, or a bare path when the directory has no `site_url` set. | `<site_url>/themes/<name>.json`. |
| `stars` | number | The repository's GitHub star count at the last refresh; `0` when GitHub could not be asked. Listings sort by it. | Same. |
| `kind` | string | `plugin`. Entries built by a directory older than the field carry `""`; the plugin installer keys on `install_type` and does not look at it. | `theme`. The theme installer requires it. |
| `screenshot_url` | string | Absent (`omitempty`). | The raw GitHub URL of `screenshot.png` (or `.jpg`) at the tag: `https://raw.githubusercontent.com/<owner>/<repo>/vX.Y.Z/screenshot.png`. |

The two entries below are copied from goblog.live's indexes on the day this page was written, with one edit: hello's live entry still says `"kind": ""` because it was built before `kind` existed, and the value the code writes today is `"plugin"`.

A plugin — [goblog-plugin-hello](https://github.com/goblogplatform/goblog-plugin-hello) v2.0.0, from `/plugins/index.json`:

```json
{
  "name": "hello",
  "display_name": "Hello",
  "description": "Appends a configurable greeting to the footer of every page. The reference goblog WebAssembly plugin.",
  "version": "2.0.0",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "source_url": "https://github.com/goblogplatform/goblog-plugin-hello",
  "download_url": "https://github.com/goblogplatform/goblog-plugin-hello/releases/download/v2.0.0/plugin.wasm",
  "sha256": "f04d6cc971b9008d61c8ee99dc1f59d5a4c6d35bd5d5e40e92f33806d0218e3b",
  "min_goblog_version": "0.2.9",
  "install_type": "wasm",
  "runtime": "wasm",
  "allowed_hosts": [],
  "released_at": "2026-09-19T22:09:16Z",
  "detail_url": "https://www.goblog.live/plugins/hello.json",
  "stars": 0,
  "kind": "plugin"
}
```

A theme — [goblog-theme-forest](https://github.com/goblogplatform/goblog-theme-forest) v1.0.0, from `/themes/index.json`:

```json
{
  "name": "forest",
  "display_name": "Forest",
  "description": "Misty greens, a soft serif and a full-bleed forest backdrop — goblog's forest theme.",
  "version": "1.0.0",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "source_url": "https://github.com/goblogplatform/goblog-theme-forest",
  "download_url": "https://github.com/goblogplatform/goblog-theme-forest/archive/refs/tags/v1.0.0.zip",
  "sha256": "d62d893981605ce2baa51fe1c2acd1ed2e3f148b23d5f6e1a14ee63d275ae95b",
  "min_goblog_version": "0.5.0",
  "install_type": "theme",
  "runtime": "",
  "allowed_hosts": [],
  "released_at": "2026-09-20T18:07:28Z",
  "detail_url": "https://www.goblog.live/themes/forest.json",
  "stars": 0,
  "kind": "theme",
  "screenshot_url": "https://raw.githubusercontent.com/goblogplatform/goblog-theme-forest/v1.0.0/screenshot.png"
}
```

An index is served with `Content-Type: application/json`, indented with two spaces, and without HTML escaping. When a directory has nothing approved for a kind, the body is `[]`.

## `<name>.json`

The detail document is the index entry with three more fields — the rendered documentation and the release history. It is what the directory's own listing page shows for an entry, published as JSON for tooling and mirrors; goblog's installer does not read it.

| Field | Type | Meaning |
|---|---|---|
| *every index field* | | As in `index.json`, same values. |
| `readme_html` | string | `README.md` at the release tag, rendered by GitHub's markdown API. |
| `changelog_html` | string | `CHANGELOG.md` at the tag, rendered the same way; `""` when the repository has none. |
| `releases` | array | Every published, non-prerelease `vX.Y.Z` release, as `ReleaseDoc` objects, newest first by publish date. |

Each element of `releases`:

| Field | Type | Meaning |
|---|---|---|
| `version` | string | The tag without `v`. |
| `released_at` | string | Publish time, RFC 3339 in UTC. |
| `notes_html` | string | The release body, rendered by GitHub's markdown API. |
| `url` | string | The release's page on GitHub. |

The HTML fields are rendered *and sanitized* by GitHub — the same renderer that draws a README on github.com — and are the only fields the directory's pages insert unescaped. Each of `readme_html` and `changelog_html` is at most 1 MiB, or the build is refused. Hello's document, abridged:

```json
{
  "name": "hello",
  "version": "2.0.0",
  "…": "the rest of the index entry",
  "readme_html": "<h1 dir=\"auto\">goblog-plugin-hello</h1>\n<p dir=\"auto\">The reference …",
  "changelog_html": "<h1 dir=\"auto\">Changelog</h1>\n<h2 dir=\"auto\">2.0.0</h2>\n<ul dir=\"auto\">…",
  "releases": [
    {
      "version": "2.0.0",
      "released_at": "2026-09-19T22:09:16Z",
      "notes_html": "<p dir=\"auto\">Rewritten as a WebAssembly plugin — sandboxed, installable from Admin → Plugins in goblog 0.2.9 or newer. …</p>",
      "url": "https://github.com/goblogplatform/goblog-plugin-hello/releases/tag/v2.0.0"
    },
    {
      "version": "1.0.0",
      "released_at": "2026-09-15T01:14:50Z",
      "notes_html": "<p dir=\"auto\">Initial release: configurable footer greeting with <code class=\"notranslate\">enabled</code> and <code class=\"notranslate\">message</code> settings.</p>",
      "url": "https://github.com/goblogplatform/goblog-plugin-hello/releases/tag/v1.0.0"
    }
  ]
}
```

## How installers verify

Both installers start from the cached index entry and refuse before touching the disk. Before any download: the `name` must match `^[a-z0-9-]+$`, `install_type` must be the right one, and `min_goblog_version` must be satisfied; a name that is already installed is refused too. `download_url` must be `https` — plain `http` is accepted only to loopback — and a redirect to anything else aborts the download.

**A plugin** (`plugin/installer`):

1. Download `download_url`, at most 16 MiB.
2. SHA-256 the bytes and compare with `sha256`, case-insensitively. A mismatch is `ErrChecksum`: "the downloaded file does not match the checksum in the directory index; the index may be stale, refresh and try again".
3. Load the module in the sandbox with the entry's `allowed_hosts`; a module that fails to load is refused.
4. Call its `identity` export: the name must equal `name` and the version must equal `version`, or the module is closed and the install refused.
5. Only then write the sidecar (the host list) and the module to `plugins/wasm/<name>.wasm`, atomically, register it, and run its `on_init`. If registration fails the files are removed again.

**A theme** (`theme/installer`):

1. Download `download_url`, at most 16 MiB.
2. Unpack the zip in memory under the archive rules in [Publishing a theme](/docs/publishing-a-theme#what-the-repository-must-contain), keeping `templates/` and `static/` and stripping the single wrapper folder GitHub puts everything under.
3. Compute the content hash and compare with `sha256`; a mismatch is refused with the theme's `ErrChecksum` text. The hash is SHA-256 over every kept file in **sorted path order**, each contributing its path, a NUL byte, its length in decimal, a NUL byte, and its bytes:

   ```text
   for path in sorted(files):
       h.write(path); h.write("\0"); h.write(str(len(bytes))); h.write("\0"); h.write(bytes)
   sha256 = hex(h.sum())
   ```

   Hashing the extracted files rather than the zip is deliberate: GitHub does not promise a tag's archive is byte-stable, and a re-compressed zip would otherwise break every install of an unchanged release.
4. Parse every `templates/*.html` on top of goblog's shared and default templates — the same check the directory ran — and refuse the theme if any file fails or is over 256 KiB.
5. Only then write the files into a temporary directory under the installed root and rename it into place as `themes/installed/<name>/`, together with a `goblog-theme.json` that records the version. An update sets the old directory aside as `<name>.prev` until the swap succeeds and restores it if it does not.

Nothing is written on a refused install, and an index that has gone stale — the directory rebuilt a release after you fetched the index — shows up as a checksum error rather than a half-installed plugin; press ↻ and try again.

## Running a private directory

The directory is a plugin compiled into every goblog, off by default. To host one:

1. **Admin → Settings**, under the *Plugin Directory* group, set `enabled` to `true`. Your site now serves `/plugins`, `/themes`, both `index.json` files, the detail documents and the submit forms. `refresh_minutes` (default 360, minimum 15) is how often listed repositories are re-checked for new releases; an optional `github_token` with no scopes lifts GitHub's API limit from 60 to 5000 requests an hour.
2. Set `site_url` to the address other goblogs will reach you at. `detail_url` is built from it; without it the field is a bare path.
3. **Admin → Plugins → Directory** — the tab appears once the plugin is enabled — and **Add repository** with a GitHub URL or `owner/name`. The repository is validated exactly as a public submission would be (the checks are in [Publishing a plugin](/docs/publishing-a-plugin#submit) and [Publishing a theme](/docs/publishing-a-theme#submit)) and listed at once, without the review step; entries submitted through `/plugins/submit` or `/themes/submit` land under **Pending review** on the same tab for you to approve or reject. **Rebuild** re-checks one entry now; **Delist** removes it.
4. On each goblog that should install from it, set `plugin_directory_url` to `https://<your site>/plugins/index.json` and `theme_directory_url` to `https://<your site>/themes/index.json` under **Admin → Settings**. The installer notices the changed URL on the next visit to **Admin → Plugins** or **Admin → Themes** and fetches the new index at once. To go back, set them to `https://www.goblog.live/plugins/index.json` and `https://www.goblog.live/themes/index.json` again.

Those two URLs are a [trust decision](/docs#trust-model): the index they point at chooses what code `Install` offers, and the checks above verify that the download matches the index, not that the index is honest. A private directory is only as trustworthy as the person adding repositories to it, and a plugin from any directory is trusted with the hosts its entry lists.

## Caching

Both indexes and every detail document are served with `Cache-Control: public, max-age=300`, so a proxy or CDN in front of a directory may hold them for five minutes. The directory itself rebuilds the index in memory whenever an entry is added, approved, rebuilt or delisted, and after every scheduled refresh.

An installer keeps one in-memory copy of its index per kind. It fetches it the first time it is needed — retrying at most every 30 seconds while nothing has been fetched yet, so a directory outage does not turn every admin page load into a hung request — and re-fetches when the copy is older than **one hour** the next time **Admin → Plugins** or **Admin → Themes** loads or an install looks an entry up, or immediately when the URL setting has changed. There is no background timer. The **↻** button next to the search box on either page forces a fetch now. A fetch that fails keeps the previous copy and logs the error; the admin page only reports an index error when it has never fetched anything, so a stale list looks like a current one — the `index_fetched_at` timestamp in the status API is what changes. Index responses are read up to 8 MiB, with a 10 second timeout.
