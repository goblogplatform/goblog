package blog

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// Post defines blog posts
type Post struct {
	ID         uint      `gorm:"primaryKey"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time
	DeletedAt  *time.Time `sql:"index"`
	Title      string     `json:"title"`
	Slug       string     `json:"slug"`
	Content    string     `sql:"type:text;" json:"content"`
	Tags       []Tag      `gorm:"many2many:post_tags" json:"tags"`
	Draft      bool       `json:"draft"`
	PostTypeID uint       `json:"post_type_id"`
	PostType   PostType   `json:"post_type" gorm:"foreignKey:PostTypeID"`
}

// Tag is used to collect Posts with similar topics
type Tag struct {
	Name  string `gorm:"primaryKey" json:"name"`
	Posts []Post `gorm:"many2many:post_tags"`
}

// Backlink tracks which posts link to other posts internally
type Backlink struct {
	SourcePostID uint `gorm:"primaryKey;index"`
	TargetPostID uint `gorm:"primaryKey;index"`
}

// ExternalBacklink tracks external sites linking to blog posts via HTTP Referer
type ExternalBacklink struct {
	ID        uint      `gorm:"primaryKey"`
	PostID    uint      `gorm:"uniqueIndex:idx_post_referer" json:"post_id"`
	Referer   string    `gorm:"uniqueIndex:idx_post_referer;type:text" json:"referer"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	HitCount  int       `json:"hit_count"`
}

// ExternalLinks extracts external URLs from the post's markdown content
func (p Post) ExternalLinks() []string {
	matches := reLinksWithURL.FindAllStringSubmatch(p.Content, -1)
	var links []string
	seen := make(map[string]bool)
	for _, match := range matches {
		href := match[2]
		if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
			if !seen[href] {
				seen[href] = true
				links = append(links, href)
			}
		}
	}
	return links
}

// PreviewContent gets a shortened version of the content for showing a preview
// https://stackoverflow.com/questions/23466497/how-to-truncate-a-string-in-a-golang-template
func (p Post) PreviewContent(length int) string {
	// This cast is O(N)
	runes := bytes.Runes([]byte(p.Content))
	if len(runes) > length {
		return string(runes[:length])
	}
	return string(runes)
}

var (
	reFencedCode = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reImages     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]+\)`)
	reLinks        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reLinksWithURL = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeadings     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic     = regexp.MustCompile(`\*(.+?)\*`)
	reUBold      = regexp.MustCompile(`__(.+?)__`)
	reUItalic    = regexp.MustCompile(`_(.+?)_`)
	reHR         = regexp.MustCompile(`(?m)^[-*_]{3,}\s*$`)
	reWhitespace = regexp.MustCompile(`\s+`)
)

// PlainTextPreview strips markdown syntax and returns clean plain text for previews
func (p Post) PlainTextPreview(length int) string {
	s := p.Content
	s = reFencedCode.ReplaceAllString(s, "$1")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reImages.ReplaceAllString(s, "$1")
	s = reLinks.ReplaceAllString(s, "$1")
	s = reHeadings.ReplaceAllString(s, "")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	s = reUBold.ReplaceAllString(s, "$1")
	s = reUItalic.ReplaceAllString(s, "$1")
	s = reHR.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	runes := bytes.Runes([]byte(s))
	if len(runes) > length {
		return string(runes[:length]) + "..."
	}
	return s
}

// HTMLPreview strips markdown syntax but preserves links as HTML hyperlinks
func (p Post) HTMLPreview(length int) template.HTML {
	s := p.Content
	s = reFencedCode.ReplaceAllString(s, "<code>$1</code>")
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = reImages.ReplaceAllString(s, "$1")
	s = reLinksWithURL.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = reHeadings.ReplaceAllString(s, "")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	s = reUBold.ReplaceAllString(s, "$1")
	s = reUItalic.ReplaceAllString(s, "$1")
	s = reHR.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	// Truncate by visible character count, skipping HTML tags
	runes := []rune(s)
	visible := 0
	cutoff := len(runes)
	inTag := false
	for i, r := range runes {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			visible++
			if visible > length {
				cutoff = i
				break
			}
		}
	}

	if cutoff < len(runes) {
		result := string(runes[:cutoff])
		if strings.Count(result, "<a ") > strings.Count(result, "</a>") {
			result += "</a>"
		}
		if strings.Count(result, "<code>") > strings.Count(result, "</code>") {
			result += "</code>"
		}
		result += "..."
		return template.HTML(result)
	}
	return template.HTML(s)
}

// Permalink returns the link to the post relative to root
func (p Post) Permalink() string {
	typeSlug := "posts"
	if p.PostType.Slug != "" {
		typeSlug = p.PostType.Slug
	}
	return "/" + typeSlug + p.CreatedAt.Format("/2006/01/02/") + p.Slug
}

func (p Post) Adminlink() string {
	typeSlug := "posts"
	if p.PostType.Slug != "" {
		typeSlug = p.PostType.Slug
	}
	return "/admin/" + typeSlug + p.CreatedAt.Format("/2006/01/02/") + p.Slug
}

func (t Tag) Permalink() string {
	return "/tag/" + t.Name
}

func (p Post) ExtractImages() []string {
	var result []string
	for _, r := range reImageRefs.FindAllStringSubmatch(p.Content, -1) {
		result = append(result, r[1])
	}
	return result
}

// postMarkdown renders a post's markdown the way the editor previews it:
// GitHub-flavoured, raw HTML kept (only admins write posts, and embeds
// depend on it).
var postMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// HTML is the post's content rendered to HTML on the server (the feed,
// and any theme that prefers server-side rendering to showdown).
func (p Post) HTML() template.HTML {
	var buf bytes.Buffer
	if err := postMarkdown.Convert([]byte(p.Content), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(p.Content))
	}
	return template.HTML(buf.String())
}

// ImageURLs is ExtractImages with every relative URL made absolute under
// site (the resolved site_url), for Open Graph and structured data.
func (p Post) ImageURLs(site string) []string {
	imgs := p.ExtractImages()
	if site == "" {
		return imgs
	}
	for i, u := range imgs {
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			continue
		}
		imgs[i] = site + "/" + strings.TrimPrefix(u, "/")
	}
	return imgs
}

// reImageRefs matches the upload widget's [file](url) and a standard
// markdown image ![alt](url); the URL stops at the first ")" or space.
var reImageRefs = regexp.MustCompile(`(?:!\[[^\]]*\]|\[file\])\(([^)\s]+)\)`)
