package blog

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"goblog/auth"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"github.com/ikeikeikeike/go-sitemap-generator/v2/stm"
)

// PageFilter is a function that decides whether a page should be shown.
// Used to filter out pages owned by disabled plugins.
type PageFilter func(page Page) bool

// builtinPageTypes are rendered by blog itself; any other page type needs a
// registered plugin to own it.
var builtinPageTypes = map[string]bool{PageTypeWriting: true, PageTypeAbout: true, PageTypeCustom: true, PageTypeTags: true, PageTypeArchives: true}

// PluginPageFilter hides pages whose type belongs to a disabled plugin or to
// no plugin at all (e.g. a plugin that was uninstalled or moved to the
// directory); such pages come back as soon as a plugin claims the type.
func PluginPageFilter(reg interface {
	HasPageType(string) bool
	IsPageTypeEnabled(string) bool
}) PageFilter {
	return func(page Page) bool {
		if builtinPageTypes[page.PageType] {
			return true
		}
		if !reg.HasPageType(page.PageType) {
			return false
		}
		return reg.IsPageTypeEnabled(page.PageType)
	}
}

// pluginRegistry is the part of the plugin registry the blog needs to
// resolve and render plugin-owned pages. It is looked up from the Gin
// context (set by plugin.Middleware) so blog does not import plugin.
type pluginRegistry interface {
	RenderPluginPage(c *gin.Context, pageType, subPath string) (string, gin.H, bool)
	HasPageType(pageType string) bool
	IsPageTypeEnabled(pageType string) bool
	Search(c *gin.Context, query string) []SearchHit
	SitemapURLs(c *gin.Context) []SitemapURL
}

// SitemapURL is one entry a plugin adds to the sitemap (see
// plugin.Sitemapper): a site-relative path and, when known, its last
// change.
type SitemapURL struct {
	Loc     string
	LastMod time.Time
}

// SearchHit is one hit a plugin contributes to the search page (see
// plugin.Searcher; plugin.SearchResult is an alias of this type). Summary
// is plain text; it is escaped when it becomes a SearchResult.
type SearchHit struct {
	Title   string
	URL     string
	Summary string
	Kind    string // short label shown next to the result, e.g. "Plugin"
}

// SearchResult is one entry on the search page — a post or a plugin hit —
// as the shared _search_results partial renders it. Date and Tags are set
// for posts only; Kind is "" for posts.
type SearchResult struct {
	Title   string
	URL     string
	Summary template.HTML
	Kind    string
	Date    time.Time
	Tags    []Tag
}

// searchResults merges matching posts (first) and plugin hits into one
// list for the search page.
func searchResults(posts []Post, hits []SearchHit) []SearchResult {
	results := make([]SearchResult, 0, len(posts)+len(hits))
	for _, p := range posts {
		results = append(results, SearchResult{Title: p.Title, URL: p.Permalink(), Summary: p.HTMLPreview(200), Date: p.CreatedAt, Tags: p.Tags})
	}
	for _, h := range hits {
		results = append(results, SearchResult{Title: h.Title, URL: h.URL, Summary: template.HTML(template.HTMLEscapeString(h.Summary)), Kind: h.Kind})
	}
	return results
}

func pluginRegistryFrom(c *gin.Context) pluginRegistry {
	if reg, exists := c.Get("plugin_registry"); exists {
		if r, ok := reg.(pluginRegistry); ok {
			return r
		}
	}
	return nil
}

// Blog API handles non-admin functions of the blog like listing posts, tags
// comments, etc.
type Blog struct {
	db             **gorm.DB // needs a double pointer to be able to update the db
	auth           auth.IAuth
	Version        string
	PageFilter     PageFilter // optional filter set by plugin system
	commentLimiter map[string]time.Time
	limiterMu      sync.Mutex
	// commentTokenKey signs the anti-spam tokens embedded in comment forms.
	// Generated per process, so tokens do not survive a restart.
	commentTokenKey []byte
}

// New constructs a Blog API
func New(db *gorm.DB, auth auth.IAuth, version string) Blog {
	api := Blog{
		db:             &db,
		auth:           auth,
		Version:        version,
		commentLimiter: make(map[string]time.Time),
	}
	api.commentTokenKey = make([]byte, 32)
	if _, err := rand.Read(api.commentTokenKey); err != nil {
		log.Fatalf("failed to generate comment token key: %v", err)
	}
	return api
}

func (b *Blog) UpdateDb(db *gorm.DB) {
	b.db = &db
}

func (b *Blog) IsDbNil() bool {
	return (*b.db) == nil
}

// Render wraps c.HTML with plugin data injection. If a plugin registry
// is available on the Gin context, it enriches the template data with
// plugin_head_html, plugin_footer_html, and plugins data.
// Render executes templateName with data, after adding what the shared
// <head> needs on every page — site_url (resolved, see SiteURL) and
// canonical_url (site_url plus canonical_path, a post's permalink, or the
// request path without its query) — and the enabled plugins' template
// data. A handler that has already set a key keeps it.
func (b *Blog) Render(c *gin.Context, code int, templateName string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	site := b.SiteURL(c)
	if _, ok := data["site_url"]; !ok {
		data["site_url"] = site
	}
	if _, ok := data["canonical_url"]; !ok {
		path, _ := data["canonical_path"].(string)
		if post, ok := data["post"].(*Post); ok && path == "" && post != nil {
			path = post.Permalink() // a post answers at several URLs
		}
		if path == "" {
			path = c.Request.URL.Path
		}
		data["canonical_url"] = site + path
	}
	if reg, exists := c.Get("plugin_registry"); exists {
		type injector interface {
			InjectTemplateData(c *gin.Context, templateName string, data gin.H) gin.H
		}
		if r, ok := reg.(injector); ok {
			data = r.InjectTemplateData(c, templateName, data)
		}
	}
	c.HTML(code, templateName, data)
}

// Generic Functions (not JSON or HTML)
func (b *Blog) GetPosts(drafts bool) []Post {
	var posts []Post
	if !drafts {
		(*b.db).Preload("Tags").Preload("PostType").Order("created_at desc").Find(&posts, "draft = ?", drafts)
	} else {
		(*b.db).Preload("Tags").Preload("PostType").Order("created_at desc").Find(&posts)
	}
	return posts
}

func (b *Blog) GetLatest() Post {
	var post Post
	(*b.db).Preload("Tags").Preload("PostType").Where("draft = ?", false).Order("created_at desc").First(&post)
	return post
}

// GetRecentPosts returns the newest n posts, drafts included, for the admin
// dashboard.
func (b *Blog) GetRecentPosts(n int) []Post {
	var posts []Post
	(*b.db).Preload("PostType").Order("created_at desc").Limit(n).Find(&posts)
	return posts
}

// CountPosts returns how many posts are published and how many are drafts.
func (b *Blog) CountPosts() (published, drafts int64) {
	(*b.db).Model(&Post{}).Where("draft = ?", false).Count(&published)
	(*b.db).Model(&Post{}).Where("draft = ?", true).Count(&drafts)
	return published, drafts
}

// CountPages returns the number of pages, enabled or not.
func (b *Blog) CountPages() int64 {
	var n int64
	(*b.db).Model(&Page{}).Count(&n)
	return n
}

// CountComments returns the number of comments across all posts.
func (b *Blog) CountComments() int64 {
	var n int64
	(*b.db).Model(&Comment{}).Count(&n)
	return n
}

func (b *Blog) getTags() []Tag {
	var tags []Tag
	(*b.db).Preload("Posts").Order("name asc").Find(&tags)
	return tags
}

// getTopTags returns the most-used tags sorted by post count descending, limited to n.
func (b *Blog) getTopTags(n int) []Tag {
	tags := b.getTags()
	sort.Slice(tags, func(i, j int) bool {
		return len(tags[i].Posts) > len(tags[j].Posts)
	})
	if len(tags) > n {
		tags = tags[:n]
	}
	return tags
}

func (b *Blog) getArchivesByYear() ([]string, map[string][]Post) {
	archive := make(map[string][]Post)
	posts := b.GetPosts(false)
	for _, post := range posts {
		year := strconv.Itoa(post.CreatedAt.Year())
		if _, ok := archive[year]; !ok {
			archive[year] = make([]Post, 0)
		}
		archive[year] = append(archive[year], post)
	}
	keys := make([]string, 0, len(archive))
	for k := range archive {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, archive
}

func (b *Blog) getArchivesByYearMonth() ([]string, map[string][]Post) {
	archive := make(map[string][]Post)
	posts := b.GetPosts(false)
	for _, post := range posts {
		year := strconv.Itoa(post.CreatedAt.Year())
		month := fmt.Sprintf("%02d", int(post.CreatedAt.Month()))
		yearMonth := year + "/" + month
		if _, ok := archive[yearMonth]; !ok {
			archive[yearMonth] = make([]Post, 0)
		}
		archive[yearMonth] = append(archive[yearMonth], post)
	}
	keys := make([]string, 0, len(archive))
	for k := range archive {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, archive
}

func (b *Blog) GetPostObject(c *gin.Context) (*Post, error) {
	var post Post
	year, err := strconv.Atoi(c.Param("yyyy"))
	if err != nil {
		return nil, errors.New("year must be an integer")
	}
	month, err := strconv.Atoi(c.Param("mm"))
	if err != nil {
		return nil, errors.New("month must be an integer")
	}
	day, err := strconv.Atoi(c.Param("dd"))
	if err != nil {
		return nil, errors.New("day must be an integer")
	}
	slug := c.Param("slug")
	slug = url.QueryEscape(slug)

	log.Println("Looking for post: ", year, "/", month, "/", day, "/", slug)

	if err := (*b.db).Preload("Tags").Preload("PostType").Where("created_at > ? AND slug LIKE ?", time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), slug).First(&post).Error; err != nil {
		return nil, errors.New("No post at " + strconv.Itoa(year) + "/" + strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + slug)
	}

	//b.db.Model(&post).Related(&post.Tags, "Tags")
	log.Println("Found: ", post.Title, " TAGS: ", post.Tags)
	return &post, nil
}

func (b *Blog) getPostByParams(year int, month int, day int, slug string) (*Post, error) {
	log.Println("trying: " + strconv.Itoa(year) + "/" + strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + slug)
	var post Post
	slug = url.QueryEscape(slug)
	if err := (*b.db).Preload("Tags").Preload("PostType").Where("created_at > ? AND slug LIKE ?", time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), slug).First(&post).Error; err != nil {
		log.Println("NOT FOUND")
		return nil, errors.New("No post at " + strconv.Itoa(year) + "/" + strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + slug)
	}
	log.Println("Found: ", post.Title, " TAGS: ", post.Tags)
	return &post, nil
}

func (b *Blog) getPostsByTag(c *gin.Context) ([]Post, error) {
	var posts []Post
	var tag Tag
	name := strings.TrimPrefix(c.Param("name"), "/")
	if err := (*b.db).Where("name = ?", name).First(&tag).Error; err != nil {
		return nil, errors.New("No tag named " + name)
	}

	(*b.db).Model(&tag).Order("created_at desc").Association("Posts").Find(&posts)
	// Batch-load PostType for all posts to avoid N+1 queries
	if len(posts) > 0 {
		ids := make([]uint, len(posts))
		for i, p := range posts {
			ids[i] = p.ID
		}
		(*b.db).Preload("PostType").Where("id IN ?", ids).Order("created_at desc").Find(&posts)
	}
	log.Print("POSTS: ", posts)
	return posts, nil
}

func (b *Blog) GetSettings() map[string]Setting {
	var settings []Setting
	(*b.db).Find(&settings)

	settingsMap := make(map[string]Setting)
	for _, setting := range settings {
		settingsMap[setting.Key] = setting
	}
	return settingsMap
}

// SettingValue returns one site setting, or def when the database is not
// ready, the setting is missing, or it is empty.
func (b *Blog) SettingValue(key, def string) string {
	if b.db == nil || *b.db == nil {
		return def
	}
	var s Setting
	if err := (*b.db).Where("key = ?", key).First(&s).Error; err != nil || s.Value == "" {
		return def
	}
	return s.Value
}

func (b *Blog) SearchPosts(query string) []Post {
	var posts []Post
	escaped := strings.ReplaceAll(query, "!", "!!")
	escaped = strings.ReplaceAll(escaped, "%", "!%")
	escaped = strings.ReplaceAll(escaped, "_", "!_")
	q := "%" + escaped + "%"
	// LOWER() on both sides keeps the search case-insensitive on Postgres,
	// where LIKE is case-sensitive (SQLite and MySQL fold case already).
	(*b.db).Preload("Tags").Preload("PostType").Where("draft = ? AND (LOWER(title) LIKE LOWER(?) ESCAPE '!' OR LOWER(content) LIKE LOWER(?) ESCAPE '!')", false, q, q).Order("created_at desc").Find(&posts)
	return posts
}

// reInternalLink matches internal post URLs like /posts/2024/01/15/my-slug, /notes/2024/01/15/my-slug, or /2024/01/15/my-slug
var reInternalLink = regexp.MustCompile(`\]\(/(?:[a-z0-9-]+/)?(\d{4})/(\d{1,2})/(\d{1,2})/([^)\s]+)\)`)

// ComputeBacklinks parses a post's content for internal links and upserts backlink records.
func (b *Blog) ComputeBacklinks(post *Post) {
	// Clear existing backlinks for this source post
	(*b.db).Where("source_post_id = ?", post.ID).Delete(&Backlink{})

	matches := reInternalLink.FindAllStringSubmatch(post.Content, -1)
	seen := make(map[uint]bool)
	for _, match := range matches {
		year, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		month, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		day, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}
		slug := match[4]

		// Use exact slug match and bounded date range
		var target Post
		startOfDay := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		endOfDay := startOfDay.Add(24 * time.Hour)
		if err := (*b.db).Preload("Tags").
			Where("slug = ? AND created_at >= ? AND created_at < ?", slug, startOfDay, endOfDay).
			First(&target).Error; err != nil {
			continue
		}
		if target.ID == post.ID || seen[target.ID] {
			continue
		}
		seen[target.ID] = true
		(*b.db).Create(&Backlink{SourcePostID: post.ID, TargetPostID: target.ID})
	}
}

// GetBacklinks returns posts that link TO the given post.
func (b *Blog) GetBacklinks(postID uint) []Post {
	var backlinks []Backlink
	(*b.db).Where("target_post_id = ?", postID).Find(&backlinks)
	if len(backlinks) == 0 {
		return nil
	}
	ids := make([]uint, len(backlinks))
	for i, bl := range backlinks {
		ids[i] = bl.SourcePostID
	}
	var posts []Post
	(*b.db).Preload("PostType").Where("id IN ? AND deleted_at IS NULL", ids).Order("created_at desc").Find(&posts)
	return posts
}

// GetOutboundLinks returns posts that the given post links TO.
func (b *Blog) GetOutboundLinks(postID uint) []Post {
	var backlinks []Backlink
	(*b.db).Where("source_post_id = ?", postID).Find(&backlinks)
	if len(backlinks) == 0 {
		return nil
	}
	ids := make([]uint, len(backlinks))
	for i, bl := range backlinks {
		ids[i] = bl.TargetPostID
	}
	var posts []Post
	(*b.db).Preload("PostType").Where("id IN ? AND deleted_at IS NULL", ids).Order("created_at desc").Find(&posts)
	return posts
}

// GetExternalBacklinks returns external referers for a given post.
func (b *Blog) GetExternalBacklinks(postID uint) []ExternalBacklink {
	var backlinks []ExternalBacklink
	(*b.db).Where("post_id = ?", postID).Order("hit_count desc").Find(&backlinks)
	return backlinks
}

// normalizeHost lowercases a host, strips any port and a leading "www." so that
// the different ways a site can be addressed compare equal.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	return strings.TrimPrefix(host, "www.")
}

// IsSelfHost reports whether refHost (from a Referer header) refers to this
// site, i.e. it matches one of the given site hosts after normalization.
// IP literals and localhost are always treated as self: an operator visiting
// their own server by IP is far more likely than an external site linking from
// a bare IP.
func IsSelfHost(refHost string, siteHosts ...string) bool {
	refHost = normalizeHost(refHost)
	if refHost == "" || refHost == "localhost" || net.ParseIP(refHost) != nil {
		return true
	}
	for _, host := range siteHosts {
		if host != "" && normalizeHost(host) == refHost {
			return true
		}
	}
	return false
}

// isSelfHost applies IsSelfHost to the current request. Behind a reverse proxy
// c.Request.Host is often the upstream address (e.g. localhost:7000) rather
// than the public hostname, so the referer is also compared against
// X-Forwarded-Host (which chained proxies may turn into a comma-separated
// list) and the configured site_url.
func (b *Blog) isSelfHost(c *gin.Context, refHost string) bool {
	siteHosts := []string{c.Request.Host}
	for _, h := range strings.Split(c.GetHeader("X-Forwarded-Host"), ",") {
		siteHosts = append(siteHosts, strings.TrimSpace(h))
	}
	var siteURLSetting Setting
	if err := (*b.db).Where("key = ?", "site_url").First(&siteURLSetting).Error; err == nil {
		if siteURL, err := url.Parse(siteURLSetting.Value); err == nil {
			siteHosts = append(siteHosts, siteURL.Host)
		}
	}
	return IsSelfHost(refHost, siteHosts...)
}

// TrackReferer records external referers for a post.
func (b *Blog) TrackReferer(c *gin.Context, postID uint) {
	referer := c.Request.Referer()
	if referer == "" {
		return
	}

	parsed, err := url.Parse(referer)
	if err != nil {
		return
	}

	// Skip self-referrals
	if b.isSelfHost(c, parsed.Hostname()) {
		return
	}

	// Skip non-HTTP(S) schemes
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return
	}

	now := time.Now()
	var existing ExternalBacklink
	result := (*b.db).Where("post_id = ? AND referer = ?", postID, referer).First(&existing)
	if result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			log.Printf("Error querying external backlinks: %v", result.Error)
			return
		}
		// New referer
		(*b.db).Create(&ExternalBacklink{
			PostID:    postID,
			Referer:   referer,
			FirstSeen: now,
			LastSeen:  now,
			HitCount:  1,
		})
	} else {
		(*b.db).Model(&existing).Updates(map[string]interface{}{
			"last_seen": now,
			"hit_count": gorm.Expr("hit_count + ?", 1),
		})
	}
}

// GetNavPages returns enabled pages that should show in the navigation, ordered by nav_order.
// Pages owned by disabled plugins are filtered out.
func (b *Blog) GetNavPages() []Page {
	var pages []Page
	(*b.db).Where("enabled = ? AND show_in_nav = ?", true, true).Order("nav_order asc").Find(&pages)
	if b.PageFilter != nil {
		var filtered []Page
		for _, p := range pages {
			if b.PageFilter(p) {
				filtered = append(filtered, p)
			}
		}
		return filtered
	}
	return pages
}

// GetPageBySlug returns a page by its slug. Returns error if not found or disabled.
func (b *Blog) GetPageBySlug(slug string) (*Page, error) {
	var page Page
	if err := (*b.db).Where("slug = ? AND enabled = ?", slug, true).First(&page).Error; err != nil {
		return nil, errors.New("page not found: " + slug)
	}
	return &page, nil
}

// GetPostTypes returns all post types
func (b *Blog) GetPostTypes() []PostType {
	var types []PostType
	(*b.db).Order("name asc").Find(&types)
	return types
}

// GetPostTypeBySlug returns a post type by its slug
func (b *Blog) GetPostTypeBySlug(slug string) (*PostType, error) {
	var pt PostType
	if err := (*b.db).Where("slug = ?", slug).First(&pt).Error; err != nil {
		return nil, errors.New("post type not found: " + slug)
	}
	return &pt, nil
}

// GetPostsByType returns posts filtered by post type
func (b *Blog) GetPostsByType(postTypeID uint, drafts bool) []Post {
	var posts []Post
	if !drafts {
		(*b.db).Preload("Tags").Preload("PostType").Where("post_type_id = ? AND draft = ?", postTypeID, false).Order("created_at desc").Find(&posts)
	} else {
		(*b.db).Preload("Tags").Preload("PostType").Where("post_type_id = ?", postTypeID).Order("created_at desc").Find(&posts)
	}
	return posts
}

// getPostByTypeAndParams finds a post by type slug and date/slug params
func (b *Blog) getPostByTypeAndParams(typeSlug string, year int, month int, day int, slug string) (*Post, error) {
	pt, err := b.GetPostTypeBySlug(typeSlug)
	if err != nil {
		return nil, err
	}
	var post Post
	slug = url.QueryEscape(slug)
	if err := (*b.db).Preload("Tags").Preload("PostType").
		Where("post_type_id = ? AND created_at > ? AND slug LIKE ?", pt.ID, time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC), slug).
		First(&post).Error; err != nil {
		return nil, errors.New("No post at " + typeSlug + "/" + strconv.Itoa(year) + "/" + strconv.Itoa(month) + "/" + strconv.Itoa(day) + "/" + slug)
	}
	return &post, nil
}

// PostTypeListing renders the listing page for a post type
func (b *Blog) PostTypeListing(c *gin.Context, pt *PostType) {
	posts := b.GetPostsByType(pt.ID, false)
	b.Render(c, http.StatusOK, "post_type_listing.html", gin.H{
		"logged_in":  b.auth.IsLoggedIn(c),
		"is_admin":   b.auth.IsAdmin(c),
		"post_type":  pt,
		"posts":      posts,
		"version":    b.Version,
		"title":      pt.Name,
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// DynamicPage renders the appropriate template for a page based on its PageType.
// subPath is the request path after the page's slug; it is only ever non-empty
// for plugin-owned pages (see NoRoute) and is passed through to the plugin.
func (b *Blog) DynamicPage(c *gin.Context, page *Page, subPath string) {
	navPages := b.GetNavPages()
	switch page.PageType {
	case PageTypeWriting:
		var posts []Post
		if page.PostTypeID != nil {
			posts = b.GetPostsByType(*page.PostTypeID, false)
		} else {
			posts = b.GetPosts(false)
		}
		b.Render(c, http.StatusOK, "page_writing.html", gin.H{
			"logged_in":  b.auth.IsLoggedIn(c),
			"is_admin":   b.auth.IsAdmin(c),
			"posts":      posts,
			"page":       page,
			"version":    b.Version,
			"title":      page.Title,
			"recent":     b.GetLatest(),
			"admin_page": false,
			"settings":   b.GetSettings(),
			"nav_pages":  navPages,
		})
	case PageTypeTags:
		b.Render(c, http.StatusOK, "page_tags.html", gin.H{
			"logged_in":  b.auth.IsLoggedIn(c),
			"is_admin":   b.auth.IsAdmin(c),
			"tags":       b.getTags(),
			"page":       page,
			"version":    b.Version,
			"title":      page.Title,
			"recent":     b.GetLatest(),
			"admin_page": false,
			"settings":   b.GetSettings(),
			"nav_pages":  navPages,
		})
	case PageTypeArchives:
		yearKeys, byYear := b.getArchivesByYear()
		monthKeys, byYearMonth := b.getArchivesByYearMonth()
		b.Render(c, http.StatusOK, "page_archives.html", gin.H{
			"logged_in":     b.auth.IsLoggedIn(c),
			"is_admin":      b.auth.IsAdmin(c),
			"yearKeys":      yearKeys,
			"byYear":        byYear,
			"yearMonthKeys": monthKeys,
			"byYearMonth":   byYearMonth,
			"page":          page,
			"version":       b.Version,
			"title":         page.Title,
			"recent":        b.GetLatest(),
			"admin_page":    false,
			"settings":      b.GetSettings(),
			"nav_pages":     navPages,
		})
	default:
		// Check if a plugin handles this page type
		if r := pluginRegistryFrom(c); r != nil {
			tmpl, pluginData, handled := r.RenderPluginPage(c, page.PageType, subPath)
			if handled {
				if tmpl == "" {
					return // the plugin wrote the response itself (e.g. JSON)
				}
				data := gin.H{
					"logged_in":  b.auth.IsLoggedIn(c),
					"is_admin":   b.auth.IsAdmin(c),
					"page":       page,
					"version":    b.Version,
					"title":      page.Title,
					"recent":     b.GetLatest(),
					"admin_page": false,
					"settings":   b.GetSettings(),
					"nav_pages":  navPages,
				}
				for k, v := range pluginData {
					data[k] = v
				}
				// A plugin sub-page (a plugin's own page in the directory, one
				// doc) names itself: the theme's page heading shows it instead
				// of the page row's title. The row is untouched.
				if title, ok := pluginData["page_title"].(string); ok && title != "" {
					named := *page
					named.Title = title
					data["page"] = &named
				}
				b.Render(c, http.StatusOK, tmpl, data)
				return
			}
			if r.HasPageType(page.PageType) {
				if r.IsPageTypeEnabled(page.PageType) {
					// The plugin is on but declined the request (an unknown sub-path).
					b.renderNotFound(c)
					return
				}
				// Plugin owns this page type but is disabled — show 404
				b.Render(c, http.StatusNotFound, "error.html", gin.H{
					"error":       "Page Not Available",
					"description": "This page is currently disabled.",
					"version":     b.Version,
					"title":       "Not Available",
					"recent":      b.GetLatest(),
					"admin_page":  false,
					"settings":    b.GetSettings(),
					"nav_pages":   navPages,
				})
				return
			}
		}
		if !builtinPageTypes[page.PageType] {
			// A plugin page type nobody owns (the plugin was uninstalled or,
			// like scholar, moved to the directory). The row stays so the page
			// comes back when a plugin claims the type again.
			b.Render(c, http.StatusNotFound, "error.html", gin.H{
				"error":       "Page Not Available",
				"description": "This page's plugin is not installed.",
				"version":     b.Version,
				"title":       "Not Available",
				"recent":      b.GetLatest(),
				"admin_page":  false,
				"settings":    b.GetSettings(),
				"nav_pages":   navPages,
			})
			return
		}
		// Fallback: render as custom content page
		b.Render(c, http.StatusOK, "page_content.html", gin.H{
			"logged_in":  b.auth.IsLoggedIn(c),
			"is_admin":   b.auth.IsAdmin(c),
			"page":       page,
			"version":    b.Version,
			"title":      page.Title,
			"recent":     b.GetLatest(),
			"admin_page": false,
			"settings":   b.GetSettings(),
			"nav_pages":  navPages,
		})
	}
}

// renderAdminPost renders the admin edit view for a post, used by NoRoute for admin type-prefixed URLs
func (b *Blog) renderAdminPost(c *gin.Context, post *Post) {
	if !b.RequireAdminPage(c) {
		return
	}
	b.Render(c, http.StatusOK, "post-admin.html", gin.H{
		"logged_in":          b.auth.IsLoggedIn(c),
		"is_admin":           b.auth.IsAdmin(c),
		"post":               post,
		"post_types":         b.GetPostTypes(),
		"version":            b.Version,
		"recent":             b.GetLatest(),
		"admin_page":         true,
		"settings":           b.GetSettings(),
		"backlinks":          b.GetBacklinks(post.ID),
		"outbound_links":     b.GetOutboundLinks(post.ID),
		"external_backlinks": b.GetExternalBacklinks(post.ID),
		"nav_pages":          b.GetNavPages(),
	})
}

// renderPost renders a single post page, used by NoRoute handlers
func (b *Blog) renderPost(c *gin.Context, post *Post) {
	b.TrackReferer(c, post.ID)
	if b.auth.IsAdmin(c) {
		b.Render(c, http.StatusOK, "post-admin.html", gin.H{
			"logged_in":              b.auth.IsLoggedIn(c),
			"is_admin":               b.auth.IsAdmin(c),
			"post":                   post,
			"post_types":             b.GetPostTypes(),
			"version":                b.Version,
			"recent":                 b.GetLatest(),
			"admin_page":             false,
			"settings":               b.GetSettings(),
			"comments":               b.getCommentsByPostID(post.ID),
			"comment_error":          c.Query("comment_error"),
			"comment_token":          b.CommentToken(post.ID),
			"comment_user":           b.auth.CurrentUser(c),
			"comments_require_login": b.CommentsRequireLogin(),
			"backlinks":              b.GetBacklinks(post.ID),
			"outbound_links":         b.GetOutboundLinks(post.ID),
			"external_backlinks":     b.GetExternalBacklinks(post.ID),
			"nav_pages":              b.GetNavPages(),
		})
	} else {
		b.Render(c, http.StatusOK, "post.html", gin.H{
			"logged_in":              b.auth.IsLoggedIn(c),
			"is_admin":               b.auth.IsAdmin(c),
			"post":                   post,
			"version":                b.Version,
			"recent":                 b.GetLatest(),
			"admin_page":             false,
			"settings":               b.GetSettings(),
			"comments":               b.getCommentsByPostID(post.ID),
			"comment_error":          c.Query("comment_error"),
			"comment_token":          b.CommentToken(post.ID),
			"comment_user":           b.auth.CurrentUser(c),
			"comments_require_login": b.CommentsRequireLogin(),
			"nav_pages":              b.GetNavPages(),
		})
	}
}

//////JSON API///////

// ListPosts lists all blog posts
func (b *Blog) ListPosts(c *gin.Context) {
	c.JSON(http.StatusOK, b.GetPosts(false))
}

// GetPost returns a post with yyyy/mm/dd/slug
func (b *Blog) GetPost(c *gin.Context) {
	post, err := b.GetPostObject(c)
	if err != nil {
		log.Println("Bad request in GetPost: " + err.Error())
		c.JSON(http.StatusBadRequest, err)
	}
	if post == nil {
		c.JSON(http.StatusNotFound, "Post Not Found")
	}
	c.JSON(http.StatusOK, post)
}

//////HTML API///////

// NoRoute returns a custom 404 page
func (b *Blog) NoRoute(c *gin.Context) {

	tokens := strings.Split(c.Request.URL.String(), "/")
	// for some reason, first token is empty

	// Try admin type-prefixed post URL: /admin/{type-slug}/{yyyy}/{mm}/{dd}/{post-slug}
	if len(tokens) >= 7 && tokens[1] == "admin" {
		typeSlug := tokens[2]
		year, yerr := strconv.Atoi(tokens[3])
		month, merr := strconv.Atoi(tokens[4])
		day, derr := strconv.Atoi(tokens[5])
		if yerr == nil && merr == nil && derr == nil {
			post, err := b.getPostByTypeAndParams(typeSlug, year, month, day, tokens[6])
			if err == nil && post != nil {
				b.renderAdminPost(c, post)
				return
			}
		}
	}

	// Try type-prefixed post URL: /{type-slug}/{yyyy}/{mm}/{dd}/{post-slug}
	if len(tokens) >= 6 {
		typeSlug := tokens[1]
		year, yerr := strconv.Atoi(tokens[2])
		month, merr := strconv.Atoi(tokens[3])
		day, derr := strconv.Atoi(tokens[4])
		if yerr == nil && merr == nil && derr == nil {
			post, err := b.getPostByTypeAndParams(typeSlug, year, month, day, tokens[5])
			if err == nil && post != nil {
				b.renderPost(c, post)
				return
			}
		}
	}

	// Backward compat: /{yyyy}/{mm}/{dd}/{slug} (any type)
	if len(tokens) >= 5 {
		year, _ := strconv.Atoi(tokens[1])
		month, _ := strconv.Atoi(tokens[2])
		day, _ := strconv.Atoi(tokens[3])
		post, err := b.getPostByParams(year, month, day, tokens[4])
		if err == nil && post != nil {
			b.renderPost(c, post)
			return
		}
	}

	// Try to resolve as a dynamic page or post type listing by slug. Only
	// plugin-owned pages own what is under their slug (/plugins/hello,
	// /plugins/index.json); built-in pages and post types are single-segment.
	path := strings.TrimPrefix(c.Request.URL.Path, "/")
	path = strings.TrimSuffix(path, "/")
	slug, subPath, _ := strings.Cut(path, "/")
	if slug != "" {
		// Check dynamic page first (pages have hero content, edit links, etc.)
		page, err := b.GetPageBySlug(slug)
		if err == nil && page != nil {
			if subPath == "" {
				b.DynamicPage(c, page, "")
				return
			}
			if r := pluginRegistryFrom(c); r != nil && r.HasPageType(page.PageType) {
				b.DynamicPage(c, page, subPath)
				return
			}
		}

		// Then try post type listing
		if subPath == "" {
			pt, err := b.GetPostTypeBySlug(slug)
			if err == nil && pt != nil {
				b.PostTypeListing(c, pt)
				return
			}
		}
	}

	b.renderNotFound(c)
}

// renderNotFound renders the generic 404 page.
func (b *Blog) renderNotFound(c *gin.Context) {
	b.Render(c, http.StatusNotFound, "error.html", gin.H{
		"logged_in":   b.auth.IsLoggedIn(c),
		"is_admin":    b.auth.IsAdmin(c),
		"error":       "404: Page Not Found",
		"description": "The page at '" + c.Request.URL.String() + "' was not found",
		"version":     b.Version,
		"recent":      b.GetLatest(),
		"admin_page":  false,
		"settings":    b.GetSettings(),
		"nav_pages":   b.GetNavPages(),
	})
}

// Home returns html of the home page using the template
// if people want to have different stuff show on the home page they probably
// need to modify this function
func (b *Blog) Home(c *gin.Context) {
	b.checkValidDb(c)
	settings := b.GetSettings()
	title := "Home"
	if subtitle, ok := settings["site_subtitle"]; ok && subtitle.Value != "" {
		title = subtitle.Value
	}
	b.Render(c, http.StatusOK, "home.html", gin.H{
		"logged_in":    b.auth.IsLoggedIn(c),
		"is_admin":     b.auth.IsAdmin(c),
		"version":      b.Version,
		"title":        title,
		"is_home":      true,
		"recent":       b.GetLatest(),
		"recent_posts": b.GetPosts(false),
		"tags":         b.getTopTags(20),
		"admin_page":   false,
		"settings":     settings,
		"nav_pages":    b.GetNavPages(),
	})
}

// Posts is the index page for blog posts
func (b *Blog) Posts(c *gin.Context) {
	b.Render(c, http.StatusOK, "posts.html", gin.H{
		"logged_in":  b.auth.IsLoggedIn(c),
		"is_admin":   b.auth.IsAdmin(c),
		"posts":      b.GetPosts(false),
		"version":    b.Version,
		"title":      "Posts",
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// Post is the page for all individual posts
func (b *Blog) Post(c *gin.Context) {
	post, err := b.GetPostObject(c)
	if err != nil {
		b.Render(c, http.StatusNotFound, "error.html", gin.H{
			"error":       "Post Not Found",
			"description": err.Error(),
			"version":     b.Version,
			"title":       "Post Not Found",
			"recent":      b.GetLatest(),
			"admin_page":  false,
			"settings":    b.GetSettings(),
			"nav_pages":   b.GetNavPages(),
		})
	} else {
		b.TrackReferer(c, post.ID)
		data := gin.H{
			"logged_in":              b.auth.IsLoggedIn(c),
			"is_admin":               b.auth.IsAdmin(c),
			"post":                   post,
			"version":                b.Version,
			"recent":                 b.GetLatest(),
			"admin_page":             false,
			"settings":               b.GetSettings(),
			"comments":               b.getCommentsByPostID(post.ID),
			"comment_error":          c.Query("comment_error"),
			"comment_token":          b.CommentToken(post.ID),
			"comment_user":           b.auth.CurrentUser(c),
			"comments_require_login": b.CommentsRequireLogin(),
			"nav_pages":              b.GetNavPages(),
		}
		if b.auth.IsAdmin(c) {
			data["backlinks"] = b.GetBacklinks(post.ID)
			data["outbound_links"] = b.GetOutboundLinks(post.ID)
			data["external_backlinks"] = b.GetExternalBacklinks(post.ID)
			data["post_types"] = b.GetPostTypes()
		}
		b.Render(c, http.StatusOK, "post.html", data)
		//if b.auth.IsAdmin(c) {
		//	b.Render(c, http.StatusOK, "post-admin.html", gin.H{
		//		"logged_in": b.auth.IsLoggedIn(c),
		//		"is_admin":  b.auth.IsAdmin(c),
		//		"post":      post,
		//		"version":   b.version,
		//	})
		//} else {
		//	b.Render(c, http.StatusOK, "post.html", gin.H{
		//		"logged_in": b.auth.IsLoggedIn(c),
		//		"is_admin":  b.auth.IsAdmin(c),
		//		"post":      post,
		//		"version":   b.version,
		//	})
		//}
	}
}

// Search handles the search page. "results" is the one list a theme
// renders (through the shared _search_results partial): matching posts
// first, then whatever enabled plugins contribute (plugin.Searcher).
// "posts", "plugin_results" and "result_count" remain for themes that
// still render the list themselves.
func (b *Blog) Search(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	var posts []Post
	var hits []SearchHit
	if query != "" {
		posts = b.SearchPosts(query)
		if r := pluginRegistryFrom(c); r != nil {
			hits = r.Search(c, query)
		}
	}
	b.Render(c, http.StatusOK, "search.html", gin.H{
		"logged_in":      b.auth.IsLoggedIn(c),
		"is_admin":       b.auth.IsAdmin(c),
		"noindex":        true, // result pages are not for crawlers
		"results":        searchResults(posts, hits),
		"posts":          posts,
		"plugin_results": hits,
		"result_count":   len(posts) + len(hits),
		"query":          query,
		"version":        b.Version,
		"title":          "Search",
		"recent":         b.GetLatest(),
		"admin_page":     false,
		"settings":       b.GetSettings(),
		"nav_pages":      b.GetNavPages(),
	})
}

// Tag lists all posts with a given tag
func (b *Blog) Tag(c *gin.Context) {
	tag := strings.TrimPrefix(c.Param("name"), "/")
	posts, err := b.getPostsByTag(c)
	if err != nil {
		b.Render(c, http.StatusNotFound, "error.html", gin.H{
			"error":       "Tag '" + tag + "' Not Found",
			"description": err.Error(),
			"version":     b.Version,
			"title":       "Tag '" + tag + "' Not Found",
			"recent":      b.GetLatest(),
			"admin_page":  false,
			"settings":    b.GetSettings(),
			"nav_pages":   b.GetNavPages(),
		})
	} else {
		b.Render(c, http.StatusOK, "tag.html", gin.H{
			"logged_in":  b.auth.IsLoggedIn(c),
			"is_admin":   b.auth.IsAdmin(c),
			"posts":      posts,
			"tag":        tag,
			"version":    b.Version,
			"title":      "Posts with Tag '" + tag + "'",
			"recent":     b.GetLatest(),
			"admin_page": false,
			"settings":   b.GetSettings(),
			"nav_pages":  b.GetNavPages(),
		})
	}
}

// Tags is the index page for all Tags
func (b *Blog) Tags(c *gin.Context) {
	b.Render(c, http.StatusOK, "tags.html", gin.H{
		"version":    b.Version,
		"title":      "Tags",
		"tags":       b.getTags(),
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// Speaking is the index page for presentations
func (b *Blog) Speaking(c *gin.Context) {
	b.Render(c, http.StatusOK, "presentations.html", gin.H{
		"logged_in":  b.auth.IsLoggedIn(c),
		"is_admin":   b.auth.IsAdmin(c),
		"version":    b.Version,
		"title":      "Presentations and Speaking",
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// Projects is the index page for projects / code
func (b *Blog) Projects(c *gin.Context) {
	b.Render(c, http.StatusOK, "projects.html", gin.H{
		"logged_in":  b.auth.IsLoggedIn(c),
		"is_admin":   b.auth.IsAdmin(c),
		"version":    b.Version,
		"title":      "Projects",
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// About is the about page
func (b *Blog) About(c *gin.Context) {
	b.Render(c, http.StatusOK, "about.html", gin.H{
		"logged_in":  b.auth.IsLoggedIn(c),
		"is_admin":   b.auth.IsAdmin(c),
		"version":    b.Version,
		"title":      "About",
		"recent":     b.GetLatest(),
		"admin_page": false,
		"settings":   b.GetSettings(),
		"nav_pages":  b.GetNavPages(),
	})
}

// Archives shows the posts by year, month, etc.
func (b *Blog) Archives(c *gin.Context) {
	yearKeys, byYear := b.getArchivesByYear()
	monthKeys, byYearMonth := b.getArchivesByYearMonth()
	b.Render(c, http.StatusOK, "archives.html", gin.H{
		"logged_in":     b.auth.IsLoggedIn(c),
		"is_admin":      b.auth.IsAdmin(c),
		"version":       b.Version,
		"title":         "Blog Archives",
		"yearKeys":      yearKeys,
		"byYear":        byYear,
		"yearMonthKeys": monthKeys,
		"byYearMonth":   byYearMonth,
		"recent":        b.GetLatest(),
		"admin_page":    false,
		"settings":      b.GetSettings(),
		"nav_pages":     b.GetNavPages(),
	})
}

// SiteURL is the site's public origin without a trailing slash: the
// site_url setting, or — when it is unset — the requesting scheme and
// host, honouring X-Forwarded-Proto from a reverse proxy.
func (b *Blog) SiteURL(c *gin.Context) string {
	if u := strings.TrimRight(b.SettingValue("site_url", ""), "/"); u != "" {
		return u
	}
	if c == nil || c.Request == nil {
		return ""
	}
	scheme := "http"
	// Proxies may chain values ("https, http") and vary the case; the
	// first is the one the client used.
	forwarded, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Proto"), ",")
	if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(forwarded), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// Sitemap serves /sitemap.xml: the home page, every enabled page (nav or
// not), post type listings, published posts (with lastmod), tags in use,
// and whatever URLs enabled plugins list for the pages under their slugs.
// Every loc is under SiteURL.
func (b *Blog) Sitemap(c *gin.Context) {
	host := b.SiteURL(c)
	sm := stm.NewSitemap(1)
	sm.SetDefaultHost(host)
	sm.Create()

	sm.Add(stm.URL{{"loc", "/"}, {"changefreq", "weekly"}, {"priority", 1.0}})

	var pages []Page
	(*b.db).Where("enabled = ?", true).Order("nav_order asc").Find(&pages)
	for _, page := range pages {
		if b.PageFilter != nil && !b.PageFilter(page) {
			continue
		}
		sm.Add(stm.URL{{"loc", page.PagePermalink()}, {"changefreq", "weekly"}, {"priority", 0.7}})
	}

	for _, pt := range b.GetPostTypes() {
		sm.Add(stm.URL{{"loc", pt.Permalink()}, {"changefreq", "weekly"}, {"priority", 0.7}})
	}

	for _, post := range b.GetPosts(false) {
		sm.Add(stm.URL{{"loc", post.Permalink()}, {"lastmod", post.UpdatedAt}, {"changefreq", "yearly"}, {"priority", 0.55}})
	}
	for _, tag := range b.getTags() {
		if len(tag.Posts) > 0 {
			sm.Add(stm.URL{{"loc", tag.Permalink()}, {"changefreq", "weekly"}, {"priority", 0.55}})
		}
	}
	if r := pluginRegistryFrom(c); r != nil {
		for _, u := range r.SitemapURLs(c) {
			entry := stm.URL{{"loc", u.Loc}, {"changefreq", "weekly"}, {"priority", 0.6}}
			if !u.LastMod.IsZero() {
				entry = append(entry, []interface{}{"lastmod", u.LastMod})
			}
			sm.Add(entry)
		}
	}

	c.Data(http.StatusOK, "application/xml; charset=utf-8", sm.XMLContent())
}

// feedItems is how many posts the RSS feed carries.
const feedItems = 20

type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Channel rssChannel
}

type rssChannel struct {
	XMLName     xml.Name `xml:"channel"`
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	Self        rssSelf  `xml:"atom:link"`
	Items       []rssItem
}

type rssSelf struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	XMLName     xml.Name `xml:"item"`
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Description string   `xml:"description"` // the post as HTML; the encoder escapes it
	Categories  []string `xml:"category"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// RSS serves /rss.xml: an RSS 2.0 feed of the newest published posts, with
// absolute links under SiteURL and each post rendered to HTML on the server.
func (b *Blog) RSS(c *gin.Context) {
	site := b.SiteURL(c)
	feed := rssFeed{Version: "2.0", Atom: "http://www.w3.org/2005/Atom", Channel: rssChannel{
		Title:       b.SettingValue("site_title", ""),
		Link:        site + "/",
		Description: b.SettingValue("site_description", ""),
		Self:        rssSelf{Href: site + "/rss.xml", Rel: "self", Type: "application/rss+xml"},
	}}
	for i, post := range b.GetPosts(false) {
		if i == feedItems {
			break
		}
		item := rssItem{
			Title:       post.Title,
			Link:        site + post.Permalink(),
			GUID:        rssGUID{IsPermaLink: true, Value: site + post.Permalink()},
			PubDate:     post.CreatedAt.UTC().Format(time.RFC1123Z),
			Description: string(post.HTML()),
		}
		for _, t := range post.Tags {
			item.Categories = append(item.Categories, t.Name)
		}
		feed.Channel.Items = append(feed.Channel.Items, item)
	}
	out, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		c.String(http.StatusInternalServerError, "feed: %v", err)
		return
	}
	c.Data(http.StatusOK, "application/rss+xml; charset=utf-8", append([]byte(xml.Header), out...))
}

// RobotsTxt serves /robots.txt: crawlers may index everything but the
// admin, the wizard, the JSON API, sign-in and search result pages, and
// are pointed at the sitemap.
func (b *Blog) RobotsTxt(c *gin.Context) {
	c.String(http.StatusOK, "User-agent: *\nDisallow: /admin\nDisallow: /wizard\nDisallow: /api/\nDisallow: /login\nDisallow: /logout\nDisallow: /search\n\nSitemap: %s/sitemap.xml\n", b.SiteURL(c))
}

// Login to the blog
func (b *Blog) Login(c *gin.Context) {
	err := godotenv.Load(".env")
	if err != nil {
		//fall back to local config
		err = godotenv.Load("local.env")
		if err != nil {
			//todo: handle better - perhaps return error to browser
			b.Render(c, http.StatusInternalServerError, "Error loading .env file: "+err.Error(), gin.H{
				"logged_in":  b.auth.IsLoggedIn(c),
				"is_admin":   b.auth.IsAdmin(c),
				"version":    b.Version,
				"title":      "Login Configuration Error",
				"recent":     b.GetLatest(),
				"admin_page": false,
				"settings":   b.GetSettings(),
				"nav_pages":  b.GetNavPages(),
			})
			return
		}
	}

	clientID := os.Getenv("client_id")
	b.Render(c, http.StatusOK, "login.html", gin.H{
		"logged_in": b.auth.IsLoggedIn(c),
		"is_admin":  b.auth.IsAdmin(c),
		// The only page whose markup uses .btn-social, so the only one that
		// loads bootstrap-social (#630).
		"login_page":          true,
		"client_id":           clientID,
		"next":                SafeNext(c.Query("next")),
		"version":             b.Version,
		"title":               "Login",
		"email_login_enabled": b.auth.EmailLoginEnabled(),
		"recent":              b.GetLatest(),
		"admin_page":          false,
		"settings":            b.GetSettings(),
		"nav_pages":           b.GetNavPages(),
	})
}

// Logout of the blog
// RequireAdminPage guards an admin *page* — one a browser navigates to,
// as opposed to the JSON API, which answers 401 so the admin scripts can
// show the error. It reports whether the handler may continue; when it
// does not, the response is already written:
//
//   - nobody signed in: a redirect to the login page, told where to return
//     to (login carries "next" through the GitHub round trip and the email
//     code form alike);
//   - signed in but not an admin: 403 and a page saying so. Sending them to
//     a login they have already completed would be a loop.
func (b *Blog) RequireAdminPage(c *gin.Context) bool {
	if b.auth.IsAdmin(c) {
		return true
	}
	if !b.auth.IsLoggedIn(c) {
		c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(c.Request.URL.RequestURI()))
		c.Abort()
		return false
	}
	// error.html renders .description as-is when it is template.HTML, so the
	// links below work in every theme without the theme changing; the user's
	// name is theirs to choose, so it is escaped.
	who := "You are signed in with an account that is not an admin."
	if u := b.auth.CurrentUser(c); u != nil && u.Name != "" {
		who = "You are signed in as " + template.HTMLEscapeString(u.Name) + ", which is not an admin account."
	}
	b.Render(c, http.StatusForbidden, "error.html", gin.H{
		"logged_in":   true,
		"is_admin":    false,
		"error":       "Admin access required",
		"description": template.HTML(who + ` <a href="/">Go to the site</a>, or <a href="/logout">sign out</a> and sign in with an admin account.`),
		"version":     b.Version,
		"title":       "Admin access required",
		"recent":      b.GetLatest(),
		"admin_page":  false,
		"settings":    b.GetSettings(),
		"nav_pages":   b.GetNavPages(),
	})
	c.Abort()
	return false
}

// SafeNext reduces a requested post-login destination to a same-site path:
// it must start with a single "/" (so no "//host" or "/\host" scheme-relative
// URLs and no absolute URLs). Anything else becomes "/".
func SafeNext(raw string) string {
	if len(raw) < 1 || raw[0] != '/' {
		return "/"
	}
	if len(raw) > 1 && (raw[1] == '/' || raw[1] == '\\') {
		return "/"
	}
	return raw
}

// RequestOrigin is the scheme and host the client actually used, without a
// trailing slash. Unlike SiteURL it ignores the site_url setting: GitHub
// matches an OAuth redirect_uri against the callback registered for the app,
// so the origin has to be the one the visitor is on — which is what the login
// page used to send when it built the URL from window.location.
func RequestOrigin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	scheme := "http"
	// Proxies may chain values ("https, http") and vary the case; the first
	// is the one the client used.
	forwarded, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Proto"), ",")
	if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(forwarded), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// GithubAuthorizeURL builds the URL that starts GitHub's OAuth flow.
//
// redirect_uri keeps the ?next= the visitor arrived with, because that is how
// next survives the round trip: GitHub sends them back to this exact URL with
// &code= appended, and Login reads next from that query. Stripping the query
// would quietly land everyone on / after signing in.
//
// Both the redirect and the next inside it are escaped. Unescaped, a next
// containing & would end the redirect_uri value early and the rest would
// reach GitHub as further authorize parameters (#631).
func GithubAuthorizeURL(origin, clientID, next string) string {
	redirect := origin + "/login"
	if next != "" && next != "/" {
		redirect += "?next=" + url.QueryEscape(next)
	}
	return "https://github.com/login/oauth/authorize?client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirect)
}

// GithubLogin serves /login/github: it sends the visitor to GitHub rather than
// having each theme assemble the authorize URL in an inline script, where the
// escaping was wrong and had to be fixed in every theme separately (#631).
func (b *Blog) GithubLogin(c *gin.Context) {
	if err := godotenv.Load(".env"); err != nil {
		_ = godotenv.Load("local.env")
	}
	clientID := os.Getenv("client_id")
	if clientID == "" {
		// Nothing to send them to; the login page explains the situation.
		c.Redirect(http.StatusFound, "/login")
		return
	}
	next := SafeNext(c.Query("next"))
	c.Redirect(http.StatusFound, GithubAuthorizeURL(RequestOrigin(c), clientID, next))
}

func (b *Blog) Logout(c *gin.Context) {
	session := sessions.Default(c)
	session.Delete("token")
	session.Save()
	c.Redirect(http.StatusTemporaryRedirect, "/")
}

// Comment anti-spam (issue #542). Each rendered comment form carries a signed
// token "<unix-ts>.<hmac>" and an empty comment_check field that an inline
// script fills with the token reversed. Submissions without a valid token, a
// script-derived check value, or that arrive implausibly soon after the form
// was issued are rejected. This stops form-filling bots that do not run JS.
const (
	commentTokenMinAge = 3 * time.Second
	commentTokenMaxAge = 24 * time.Hour
)

func (b *Blog) commentTokenSig(ts string, postID uint) string {
	mac := hmac.New(sha256.New, b.commentTokenKey)
	mac.Write([]byte(ts + "|" + strconv.FormatUint(uint64(postID), 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// CommentTokenAt mints a comment-form token for postID issued at the given time.
func (b *Blog) CommentTokenAt(postID uint, issued time.Time) string {
	ts := strconv.FormatInt(issued.Unix(), 10)
	return ts + "." + b.commentTokenSig(ts, postID)
}

// CommentToken mints a comment-form token for postID issued now.
func (b *Blog) CommentToken(postID uint) string {
	return b.CommentTokenAt(postID, time.Now())
}

// verifyCommentToken checks a submitted token and its script-derived check
// value against postID at time now.
func (b *Blog) verifyCommentToken(token, check string, postID uint, now time.Time) bool {
	ts, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(b.commentTokenSig(ts, postID))) {
		return false
	}
	issuedUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(issuedUnix, 0))
	if age < commentTokenMinAge || age > commentTokenMaxAge {
		return false
	}
	return check == reverseString(token)
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func (b *Blog) canComment(ip string) bool {
	b.limiterMu.Lock()
	defer b.limiterMu.Unlock()
	if last, ok := b.commentLimiter[ip]; ok {
		if time.Since(last) < time.Minute {
			return false
		}
	}
	return true
}

func (b *Blog) recordComment(ip string) {
	b.limiterMu.Lock()
	defer b.limiterMu.Unlock()
	b.commentLimiter[ip] = time.Now()
}

// CommentsRequireLogin reports the comments_require_login setting. Login is
// required unless the setting exists and is explicitly "false", so a missing
// row (or a settings table that hasn't been seeded) fails closed.
func (b *Blog) CommentsRequireLogin() bool {
	var setting Setting
	if err := (*b.db).Where("key = ?", "comments_require_login").First(&setting).Error; err != nil {
		return true
	}
	return setting.Value != "false"
}

func (b *Blog) getCommentsByPostID(postID uint) []Comment {
	var comments []Comment
	(*b.db).Where("post_id = ?", postID).Order("created_at asc").Find(&comments)
	return comments
}

// GetRecentComments returns the most recent comments across all posts
func (b *Blog) GetRecentComments(limit int) []Comment {
	var comments []Comment
	(*b.db).Order("created_at desc").Limit(limit).Find(&comments)
	return comments
}

// GetComments returns a page of comments across all posts, newest first,
// along with the total number of comments.
func (b *Blog) GetComments(offset, limit int) ([]Comment, int64) {
	var total int64
	(*b.db).Model(&Comment{}).Count(&total)
	var comments []Comment
	(*b.db).Order("created_at desc").Offset(offset).Limit(limit).Find(&comments)
	return comments, total
}

// GetPostsByIDs returns a map of post ID to Post for the given IDs
func (b *Blog) GetPostsByIDs(ids []uint) map[uint]Post {
	result := make(map[uint]Post)
	if len(ids) == 0 {
		return result
	}
	var posts []Post
	(*b.db).Preload("PostType").Where("id IN ?", ids).Find(&posts)
	for _, p := range posts {
		result[p.ID] = p
	}
	return result
}

// SubmitComment handles POST /comments form submissions
func (b *Blog) SubmitComment(c *gin.Context) {
	// The form names where to go afterwards; keep it on this site.
	redirect := SafeNext(c.PostForm("redirect"))

	// Honeypot check - if website field is filled, silently redirect
	if c.PostForm("website") != "" {
		c.Redirect(http.StatusSeeOther, redirect)
		return
	}

	postIDStr := c.PostForm("post_id")
	name := strings.TrimSpace(c.PostForm("name"))
	email := strings.TrimSpace(c.PostForm("email"))
	content := strings.TrimSpace(c.PostForm("content"))

	postID, err := strconv.ParseUint(postIDStr, 10, 64)
	if err != nil || postID == 0 {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=invalid_post")
		return
	}

	// Logged-in commenters are attributed to their account: the email always
	// comes from the account (the form's value is ignored) and a blank name
	// falls back to the account's display name (issue #524).
	user := b.auth.CurrentUser(c)
	var userID *int
	if user != nil {
		email = user.Email
		userID = &user.ID
		if name == "" {
			name = user.DisplayName()
		}
	} else if b.CommentsRequireLogin() {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=login_required")
		return
	}

	if name == "" || content == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=missing_fields")
		return
	}

	if len(name) > 100 {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=name_too_long")
		return
	}
	if len(email) > 254 {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=email_too_long")
		return
	}
	if len(content) > 5000 {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=content_too_long")
		return
	}

	if !b.verifyCommentToken(c.PostForm("comment_token"), c.PostForm("comment_check"), uint(postID), time.Now()) {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=spam_check")
		return
	}

	ip := c.ClientIP()
	if !b.canComment(ip) {
		c.Redirect(http.StatusSeeOther, redirect+"?comment_error=rate_limit")
		return
	}

	comment := Comment{
		PostID:    uint(postID),
		Name:      name,
		Email:     email,
		Content:   content,
		IPAddress: ip,
		UserID:    userID,
	}
	(*b.db).Create(&comment)
	b.recordComment(ip)

	c.Redirect(http.StatusSeeOther, redirect+fmt.Sprintf("#comment-%d", comment.ID))
}

func (b *Blog) checkValidDb(c *gin.Context) {
	if b.db == nil {
		b.Render(c, http.StatusInternalServerError, "error.html", gin.H{
			"error":       "Database Not Found",
			"description": "Database is not connected",
			"version":     b.Version,
			"title":       "Database Not Found",
			"admin_page":  false,
			"settings":    b.GetSettings(),
		})
	}
}
