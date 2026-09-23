# Server-side markdown rendering — design

## Goal

A post's body is HTML by the time it leaves the server. Today `post.html`,
`post-admin.html` and `page_content.html` ship raw markdown to the browser and
render it there with showdown + DOMPurify, so anything that does not run
JavaScript — link-preview bots, most crawlers, reader modes, `curl` — sees
markdown or nothing, and the editor, the public page and the RSS feed each use
a different markdown dialect.

Issue #617.

## Evidence

Measured on the live site before designing (115 posts in production):

| source contains | posts | rendered in the browser today |
|---|---|---|
| `<iframe>` (YouTube) | 4 | **stripped** by DOMPurify |
| `<script>` (Instagram embeds) | 7 | **stripped** by DOMPurify |
| `<div>` | 8 | kept |
| `<img>` | 3 | kept |

Rendering `/posts/2017/02/02/One-Year-in-our-mobile-mesh-platform-is-taking-shape`
in headless Chrome and reading the DOM inside `#html`: zero iframes, zero
blockquotes. The embeds in those posts have not worked for as long as
DOMPurify has been in the template.

## Decisions

- **One markdown engine: goldmark with GFM.** `Post.HTML()` already exists
  (added with the RSS feed in #615) and becomes the single path from markdown
  to HTML. `Page.HTML()` and `Comment.HTML()` join it.
- **Posts and pages render raw HTML unsanitised** (goldmark `WithUnsafe`).
  Their authors are admins, and this is what makes every embed work —
  including the Instagram `<script>` tags that DOMPurify removes today.
  Accepted trade, decided by the site owner: a post or page can execute
  JavaScript on the site, so a compromised admin account is a stored-XSS
  foothold. Documented in *Writing a theme* so it is a known property rather
  than a surprise.
- **Comments are sanitised** with bluemonday's `UGCPolicy` (new dependency,
  `github.com/microcosm-cc/bluemonday`): emphasis, links, lists and code, no
  raw HTML, no scripts, no iframes. Commenters are anonymous; this is not
  configurable.
- **The editor previews through the server.** A new admin-only
  `POST /api/v1/preview` renders unsaved markdown with the same `Post.HTML()`
  path. The editor keeps SimpleMDE's toolbar, shortcuts and drag-and-drop
  upload; its live side-by-side preview is switched off and replaced with
  GitHub-style **Write | Preview** tabs. What the preview shows is what the
  published page renders, because it is the same code.
- **`.post.Content` stays.** It is a struct field; a theme that has not
  migrated keeps rendering client-side and keeps working. Nothing forces a
  theme update in this release.

## Rendering API

```go
func (p Post) HTML() template.HTML     // GFM, raw HTML kept
func (p Page) HTML() template.HTML     // same
func (c Comment) HTML() template.HTML  // GFM, then bluemonday UGCPolicy
```

All three share one `goldmark.Markdown` for posts/pages and one for comments,
built once at package level. A render error falls back to the escaped source
rather than an empty body, as `Post.HTML` does today.

`HTMLPreview` (the regex-based summariser used by listings, search results and
meta descriptions) is unchanged: it deliberately produces a short plain-ish
snippet, not a rendered document.

## Preview endpoint

`POST /api/v1/preview`, `Content-Type: application/json`, body
`{"content": "<markdown>"}` → `{"html": "<rendered>"}`.

- Admin only, through the existing JSON guard: a non-admin gets
  `401 "Not Authorized"` (the page guard from #620 does not apply — this is
  API, not a page).
- Renders with the post/page policy, because that is what the author is
  writing.
- No persistence, no size ceiling beyond Gin's existing request limits.

## Editor

`admin_new_post.html` and `post-admin.html`:

- Tabs above the editor: **Write** (the SimpleMDE textarea) and **Preview**
  (a pane filled from `/api/v1/preview` on each activation).
- SimpleMDE is constructed with its preview/side-by-side toolbar buttons
  removed, so there is one preview mechanism rather than two that disagree.
- `admin_edit_page.html` uses a plain textarea today and gets the same tabs.

## Templates

| template | today | after |
|---|---|---|
| `post.html` | showdown + DOMPurify + `<noscript>` | `{{ .post.HTML }}` |
| `post-admin.html` | same, plus the editor | `{{ .post.HTML }}` + tabbed editor |
| `page_content.html` | same, for `.page.Content` | `{{ .page.HTML }}` |
| comments in `post.html` | per-comment inline script | `{{ .HTML }}` |

The showdown and DOMPurify `<script>` tags go with them: two fewer
render-blocking CDN requests on every post and page.

Repos: `themes/default` here, then `goblog-site-theme` and
`goblog-theme-forest` (both carry copies of `post.html` and
`page_content.html`). `goblog-theme-minimal` ships neither and inherits
default's.

## Testing

- `blog`: `Post.HTML`/`Page.HTML` keep raw HTML (iframe, script) and render
  GFM (tables, fenced code, task lists); `Comment.HTML` strips `<script>`,
  `<iframe>`, `onerror=` and javascript: URLs while keeping links and
  emphasis; a malformed-input fallback.
- `admin`: the preview endpoint renders, is admin-only, and its output equals
  `Post.HTML` for the same input.
- Template tests: a served post page contains the rendered body, no
  `showdown`, no `purify`, no `<noscript>` markdown; a comment's markdown is
  rendered and its script tag is not.
- Against real content: a post whose source holds a YouTube iframe serves that
  iframe.

## Out of scope

Each is filed with the evidence behind the decision:

- **#624 — defer the CDN scripts.** Needs the inline `$(…)` and
  `hljs.highlightAll()` calls in each theme moved into `DOMContentLoaded`
  handlers first. This change already removes showdown and DOMPurify from the
  post and page templates.
- **#625 — server-side syntax highlighting.** Blocked on content: 277 code
  fences across 36 posts, and exactly one names a language. chroma colours
  what the fence declares, so switching today would grey out the archive.
  highlight.js keeps decorating `<pre><code>`, which is now in the HTML
  already.
- **#626 — cache rendered HTML in a column.** Rendering costs 0.26 ms for an
  average post and 2.4 ms for the largest, against page renders of 10–17 ms;
  a cache would be a second source of truth for a fraction of a millisecond.
