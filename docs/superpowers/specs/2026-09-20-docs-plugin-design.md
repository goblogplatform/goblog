# Documentation on goblog.live — design

## Goal

goblog.live/docs explains how to build and publish plugins and themes, with
the API references a builder needs — and it can never drift from the code,
because the pages ship inside the goblog binary that serves them.

## Decisions

- **Content is markdown in the goblog repository, embedded in the binary**
  (`plugins/docs/content/*.md`). A PR that changes an export, a limit or a
  contract changes the page in the same diff. goblog.live documents the
  version it runs; any goblog can enable the same pages.
- **A compiled-in `docs` plugin** (off by default) owns the `docs` page
  slug: `/docs` and `/docs/<slug>`, nav order 32, one `enabled` setting.
- **Server-side rendering with goldmark** (GFM + auto heading IDs): real
  HTML, linkable sections, a per-page table of contents, works without JS,
  indexable. One new dependency; the content is ours, so no sanitiser.
- The two contract docs move into the content directory; `docs/PLUGIN_CONTRACT.md`
  and `docs/THEME_CONTRACT.md` become one-line pointers so existing links
  keep working. The README's WebAssembly-plugin and Theming sections shrink
  to what an operator needs plus a link to goblog.live/docs.

## Pages (sidebar order)

| slug | title | content |
|---|---|---|
| `` (index) | Overview | what plugins and themes are; the trust model (sandboxed wasm vs. a theme is code); where the directory fits; links to the rest |
| `writing-a-plugin` | Writing a plugin | walkthrough from `goblog-plugin-hello`: manifest, `identity`, one setting, a footer hook, build command, `goblog validate-plugin`, run it locally (`plugins/wasm/<name>.wasm` + sidecar) |
| `plugin-api` | Plugin API reference | every export with input/output JSON; `ctx`; host functions and store limits; HTTP + `allowed_hosts`; timeouts and memory; `on_init` and settings semantics; pages (`render_page` forms, `sub_path`); jobs; scholar as the worked example |
| `publishing-a-plugin` | Publishing a plugin | the plugin contract (moved from `PLUGIN_CONTRACT.md`), release workflow, submit and review, updates |
| `writing-a-theme` | Writing a theme | the override-by-name loader; a minimal theme (`header.html` + CSS); template data available; `rawHTML`; static files; testing locally in `themes/installed/` |
| `publishing-a-theme` | Publishing a theme | the theme contract (moved from `THEME_CONTRACT.md`), screenshot, content hash, submit |
| `directory-formats` | Directory formats | `index.json` / `<name>.json` for both kinds field by field; `kind`/`install_type`/`runtime`; how installers verify (sha256 vs content hash); `plugin_directory_url` / `theme_directory_url` for private directories |

Every page starts with an H1 that matches the sidebar title. Pages link to
each other by relative slug (`/docs/plugin-api#exports`), never by absolute
goblog.live URL, so a self-hosted copy stays self-contained.

## Package `plugins/docs`

- `content/` embedded with `//go:embed content/*.md`.
- `pages.go`: `var pages = []page{{Slug: "", Title: "Overview", File: "overview.md"}, …}` in sidebar order; `Slug` is the URL segment.
- Rendering: goldmark configured with `extension.GFM`, `parser.WithAutoHeadingID()`, `html.WithUnsafe()` (the content is ours and includes raw HTML tables where markdown tables are too narrow). Each page is rendered once at plugin construction into `map[slug]rendered{HTML template.HTML; TOC []heading}`; a render error is a startup panic (the tests catch it first).
- Table of contents: the H2/H3 headings of the page (text + generated ID), rendered as a list at the top of the article for pages longer than three headings.
- `RenderPage(ctx, "docs")`: `SubPath == ""` → Overview; a known slug → that page; anything else → `"", nil` (404). Output is `page_content.html` with `plugin_content` = sidebar (`<nav>` with every page, current one marked) + article, wrapped in a two-column Bootstrap row that collapses on narrow screens. `title` = page title.
- `OnInit` ensures the page row (slug `docs`, type `docs`, title "Docs", nav order 32) with the same slug-collision handling as the directory plugin.
- Settings: `enabled` only (default `false`).

## Content sourcing

- `plugin-api.md` is written from the README's WebAssembly section and
  `plugin/wasm/contract.go` / `plugin/wasm/wasm.go` (the authoritative
  shapes and limits); `directory-formats.md` from `registry.IndexEntry` /
  `DetailDoc`; the two publishing pages from the existing contract docs.
- Code samples come from `goblog-plugin-hello` (walkthrough) and
  `plugin/wasm/testdata/echo/main.go` (reference) and are kept short; the
  pages link to the repos for the full files.

## Tests

- Every entry in `pages` has a file that exists and renders without error.
- Every `/docs/<slug>` link (and `#anchor` on a known slug) in the content
  resolves to a page (and, for anchors, to a heading ID on that page).
- The sidebar marks the current page; unknown slugs decline; `/docs` is the
  Overview; H2 headings get IDs.
- A content-lint test: no page mentions a setting, export or host function
  name that does not exist in code — implemented as a checklist of known
  identifiers (exports from `plugin/wasm`, setting keys) that each page's
  code spans are checked against, so a rename fails the test.

## Rollout

- goblog PR: plugin + content + README trims + contract pointers; release;
  enable `docs` on goblog.live (Admin → Settings → Docs → `enabled`); the
  Docs nav entry appears. Link it from the directory pages' footers
  ("Built a plugin? Submit it — or read the docs").
- goblog-site-theme: nothing (the page renders through `page_content.html`).

## Out of scope

- goblog's own REST API (`/api/v1/*`) — operator-internal.
- Versioned docs for older releases (the page documents the running version;
  older versions have their README).
- Search.
