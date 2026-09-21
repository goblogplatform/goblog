package admin

import (
	"errors"
	"fmt"
	"goblog/auth"
	"goblog/blog"
	gplugin "goblog/plugin"
	"goblog/plugin/installer"
	"goblog/plugins/directory"
	"goblog/theme"
	tinstaller "goblog/theme/installer"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// WWWFolder and UploadsFolder configures where the file uploads should be stored. This is
// mostly used for testing
var WWWFolder = "www/"
var UploadsFolder = "uploads/"

// Admin handles admin requests
type Admin struct {
	db            **gorm.DB // needs a double pointer to be able to update the db
	auth          auth.IAuth
	b             *blog.Blog
	version       string
	OnThemeChange func(theme string)    // callback to reload templates when theme changes
	Installer     *installer.Installer  // plugin directory install/update; nil when not wired
	Directory     *directory.Plugin     // the directory this site hosts; nil when not wired
	Themes        *tinstaller.Installer // theme directory install/activate; nil when not wired
}

// New constructs an Admin API
func New(db *gorm.DB, auth auth.IAuth, b *blog.Blog, version string) Admin {
	api := Admin{db: &db, auth: auth, b: b, version: version}
	return api
}

// ListThemes returns every theme goblog can activate: built-in and installed.
func ListThemes() []string { return theme.List() }

// pluginRegistry returns the plugin registry the plugin middleware put on
// the Gin context, or nil when none is wired.
func pluginRegistry(c *gin.Context) *gplugin.Registry {
	if reg, exists := c.Get("plugin_registry"); exists {
		if r, ok := reg.(*gplugin.Registry); ok {
			return r
		}
	}
	return nil
}

// getDisabledPluginPageTypes returns a map of page types that belong to disabled plugins.
func (a *Admin) getDisabledPluginPageTypes(c *gin.Context) map[string]bool {
	result := make(map[string]bool)
	if reg, exists := c.Get("plugin_registry"); exists {
		if r, ok := reg.(*gplugin.Registry); ok {
			for _, p := range r.Plugins() {
				for _, page := range p.Pages() {
					if !r.IsPluginEnabled(p.Name()) {
						result[page.PageType] = true
					}
				}
			}
		}
	}
	return result
}

// UpdatePluginSettings saves plugin settings via the plugin registry.
func (a *Admin) UpdatePluginSettings(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var settings []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.BindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	reg, exists := c.Get("plugin_registry")
	if !exists {
		c.JSON(http.StatusInternalServerError, "Plugin registry not available")
		return
	}
	r, ok := reg.(*gplugin.Registry)
	if !ok {
		c.JSON(http.StatusInternalServerError, "Plugin registry not available")
		return
	}

	for _, s := range settings {
		// Key format is "pluginname.settingkey"
		parts := strings.SplitN(s.Key, ".", 2)
		if len(parts) != 2 {
			continue
		}
		r.UpdateSetting(parts[0], parts[1], s.Value)
	}

	c.JSON(http.StatusAccepted, settings)
}

func (a *Admin) UpdateDb(db *gorm.DB) {
	a.db = &db
}

// ////JSON API///////
func safeSlug(slug string) string {
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "/", "")
	slug = strings.ReplaceAll(slug, ".", "-")
	return url.QueryEscape(slug)
}

// CreatePost adds a post if the user has permission
func (a *Admin) CreatePost(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestPost blog.Post
	err := c.BindJSON(&requestPost)
	if err != nil {
		log.Println("MALFORMED REQ: " + err.Error())
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if requestPost.Title == "" || requestPost.Content == "" {
		c.JSON(http.StatusBadRequest, "Missing Title or Content")
		return
	}

	//todo: make tags work - need to get the relations working
	requestPost.Slug = safeSlug(requestPost.Title)

	// Default to "posts" type if not specified
	if requestPost.PostTypeID == 0 {
		var defaultType blog.PostType
		if err := (*a.db).Where("slug = ?", "posts").First(&defaultType).Error; err != nil {
			c.JSON(http.StatusBadRequest, "Default post type not found")
			return
		}
		requestPost.PostTypeID = defaultType.ID
	}

	// Validate that the PostType exists
	var postType blog.PostType
	if err := (*a.db).First(&postType, requestPost.PostTypeID).Error; err != nil {
		c.JSON(http.StatusBadRequest, "Invalid post_type_id")
		return
	}

	log.Print("CREATING POST: ", requestPost)
	(*a.db).Create(&requestPost)

	// Reload with PostType for JSON response
	(*a.db).Preload("PostType").First(&requestPost, requestPost.ID)

	a.b.ComputeBacklinks(&requestPost)

	log.Println("POST CREATED: ", requestPost)
	c.JSON(http.StatusCreated, requestPost)
}

// UploadFile is the endpoint for storing files on the server
// https://github.com/gin-gonic/examples/blob/master/upload-file/single/main.go
func (a *Admin) UploadFile(c *gin.Context) {
	log.Println("Upload file API hit")

	// Allow the install wizard (pre-admin) to upload images during setup.
	if !a.auth.IsAdmin(c) && !a.auth.IsWizardMode(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		log.Println(fmt.Sprintf("FormFile erorr: %s", err.Error()))
		c.JSON(http.StatusBadRequest, fmt.Sprintf("get form err: %s", err.Error()))
		return
	}

	filename := UploadsFolder + filepath.Base(file.Filename)
	if err := c.SaveUploadedFile(file, WWWFolder+filename); err != nil {
		log.Println(fmt.Sprintf("Save Upload File Error erorr: %s", err.Error()))
		c.JSON(http.StatusBadRequest, fmt.Sprintf("upload file err: %s", err.Error()))
		return
	}

	log.Println("Saved file okay: " + filename)

	c.JSON(http.StatusOK, map[string]interface{}{"filename": "/" + filename})
}

// UpdatePost modifies an existing post
// Requires the ID of the post, title and content to not be empty
func (a *Admin) UpdatePost(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestPost blog.Post
	e := c.BindJSON(&requestPost)
	if e != nil {
		log.Println("MALFORMED REQUEST: " + e.Error())
		c.JSON(http.StatusBadRequest, "Malformed request, missing some information")
		return
	}
	log.Println("REQUEST POST: ", requestPost)

	if requestPost.Title == "" || requestPost.Content == "" || requestPost.ID < 0 {
		c.JSON(http.StatusBadRequest, "Missing ID, Title or Content")
		return
	}
	requestPost.Slug = safeSlug(requestPost.Slug)

	var existingPost blog.Post
	err := (*a.db).Where("id = ?", requestPost.ID).First(&existingPost).Error

	if err != nil {
		c.JSON(http.StatusBadRequest, "Existing post with ID "+fmt.Sprint(requestPost.ID)+" not found")
		return
	}

	// Save revision of the current state before updating, but only if something changed
	if existingPost.Title != requestPost.Title ||
		existingPost.Slug != requestPost.Slug ||
		existingPost.Content != requestPost.Content ||
		existingPost.Draft != requestPost.Draft ||
		existingPost.PostTypeID != requestPost.PostTypeID {
		revision := blog.PostRevision{
			PostID:     existingPost.ID,
			Title:      existingPost.Title,
			Slug:       existingPost.Slug,
			Content:    existingPost.Content,
			Draft:      existingPost.Draft,
			PostTypeID: existingPost.PostTypeID,
		}
		if err := (*a.db).Create(&revision).Error; err != nil {
			log.Println("Warning: failed to save revision: ", err)
		}
	}

	log.Println("UPDATING DRAFT AS: ", requestPost.Draft)

	existingPost.Title = requestPost.Title
	existingPost.Content = requestPost.Content
	existingPost.Slug = requestPost.Slug
	existingPost.CreatedAt = requestPost.CreatedAt
	existingPost.Draft = requestPost.Draft
	existingPost.PostTypeID = requestPost.PostTypeID

	txErr := (*a.db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&existingPost).Where("id = ?", requestPost.ID).Updates(&existingPost).Error; err != nil {
			return err
		}
		// Filter empty/whitespace tag names, deduplicate, and ensure each exists in the DB
		seen := make(map[string]bool)
		var validTags []blog.Tag
		for _, tag := range requestPost.Tags {
			name := strings.TrimSpace(tag.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			var t blog.Tag
			if err := tx.Where("name = ?", name).FirstOrCreate(&t, blog.Tag{Name: name}).Error; err != nil {
				return err
			}
			validTags = append(validTags, t)
		}
		// Replace tag associations atomically to avoid duplication
		if err := tx.Model(&existingPost).Association("Tags").Replace(validTags); err != nil {
			return err
		}
		//https://stackoverflow.com/questions/56653423/gorm-doesnt-update-boolean-field-to-false
		if !requestPost.Draft {
			if err := tx.Model(&existingPost).Select("draft").Update("draft", false).Error; err != nil {
				return err
			}
		}
		// Explicitly update post_type_id since GORM's struct-based Updates may skip it
		if err := tx.Model(&existingPost).Select("post_type_id").Update("post_type_id", requestPost.PostTypeID).Error; err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		log.Println("ERROR UPDATING POST: ", txErr)
		c.JSON(http.StatusInternalServerError, "Failed to update post")
		return
	}

	// Reload into a fresh struct to avoid GORM appending to existing slices
	var updatedPost blog.Post
	if err := (*a.db).Preload("PostType").Preload("Tags").First(&updatedPost, existingPost.ID).Error; err != nil {
		log.Println("ERROR RELOADING POST: ", err)
		c.JSON(http.StatusInternalServerError, "Failed to reload post after update")
		return
	}

	a.b.ComputeBacklinks(&updatedPost)

	log.Println("POST UPDATED: ", updatedPost)
	c.JSON(http.StatusAccepted, updatedPost)
}

// DeletePost deletes a post from the database
func (a *Admin) DeletePost(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestPost blog.Post
	if c.BindJSON(&requestPost) != nil {
		c.JSON(http.StatusBadRequest, "Malformed request, missing some information")
		return
	}

	(*a.db).Where("id = ?", requestPost.ID).Delete(&blog.Post{})

	c.JSON(http.StatusOK, "")
}

// ListRevisions returns the revision history for a post
func (a *Admin) ListRevisions(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	id := c.Param("id")
	var revisions []blog.PostRevision
	(*a.db).Where("post_id = ?", id).Order("created_at desc").Find(&revisions)
	c.JSON(http.StatusOK, revisions)
}

// RollbackRevision restores a post to a previous revision
func (a *Admin) RollbackRevision(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	postID := c.Param("id")
	revisionID := c.Param("revisionId")

	var revision blog.PostRevision
	if err := (*a.db).Where("id = ? AND post_id = ?", revisionID, postID).First(&revision).Error; err != nil {
		c.JSON(http.StatusNotFound, "Revision not found")
		return
	}

	var existingPost blog.Post
	if err := (*a.db).Where("id = ?", postID).First(&existingPost).Error; err != nil {
		c.JSON(http.StatusNotFound, "Post not found")
		return
	}

	txErr := (*a.db).Transaction(func(tx *gorm.DB) error {
		// Save current state as a revision before rolling back
		currentRevision := blog.PostRevision{
			PostID:     existingPost.ID,
			Title:      existingPost.Title,
			Slug:       existingPost.Slug,
			Content:    existingPost.Content,
			Draft:      existingPost.Draft,
			PostTypeID: existingPost.PostTypeID,
		}
		if err := tx.Create(&currentRevision).Error; err != nil {
			return err
		}

		// Apply the revision
		existingPost.Title = revision.Title
		existingPost.Slug = revision.Slug
		existingPost.Content = revision.Content
		existingPost.Draft = revision.Draft
		existingPost.PostTypeID = revision.PostTypeID
		if err := tx.Save(&existingPost).Error; err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		log.Println("ERROR ROLLING BACK POST: ", txErr)
		c.JSON(http.StatusInternalServerError, "Failed to rollback post")
		return
	}

	// Reload with associations
	var updatedPost blog.Post
	if err := (*a.db).Preload("PostType").Preload("Tags").First(&updatedPost, existingPost.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, "Failed to reload post after rollback")
		return
	}
	a.b.ComputeBacklinks(&updatedPost)

	c.JSON(http.StatusAccepted, updatedPost)
}

// DeleteComment deletes a comment from the database
func (a *Admin) DeleteComment(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestComment blog.Comment
	if c.BindJSON(&requestComment) != nil {
		c.JSON(http.StatusBadRequest, "Malformed request, missing some information")
		return
	}

	(*a.db).Where("id = ?", requestComment.ID).Delete(&blog.Comment{})

	c.JSON(http.StatusOK, "")
}

func (a *Admin) PublishPost(c *gin.Context) {
	id := c.Param("id")
	log.Println("Publishing post: ", id)

	var post blog.Post
	(*a.db).Preload("PostType").Where("id = ?", id).First(&post)
	(*a.db).Model(&post).Select("draft").Update("draft", false)
	c.JSON(http.StatusAccepted, post)
}

func (a *Admin) DraftPost(c *gin.Context) {
	id := c.Param("id")
	log.Println("Drafting post: ", id)
	var post blog.Post
	(*a.db).Preload("PostType").Where("id = ?", id).First(&post)
	(*a.db).Model(&post).Select("draft").Update("draft", true)
	c.JSON(http.StatusAccepted, post)
}

func (a *Admin) AddSetting(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestSetting blog.Setting
	err := c.BindJSON(&requestSetting)
	if err != nil {
		log.Println("MALFORMED REQ: " + err.Error())
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if requestSetting.Key == "" {
		c.JSON(http.StatusBadRequest, "Missing Key Name for Setting")
		return
	}

	log.Print("CREATING SETTING: ", requestSetting)
	(*a.db).Create(&requestSetting)

	log.Println("SETTING CREATED: ", requestSetting)
	c.JSON(http.StatusCreated, requestSetting)
}

func (a *Admin) UpdateSettings(c *gin.Context) {
	contentType := c.Request.Header.Get("content-type")
	if contentType != "application/json" {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	// Allow the install wizard (pre-admin) to write the initial settings.
	if !a.auth.IsAdmin(c) && !a.auth.IsWizardMode(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestSettings []blog.Setting
	err := c.BindJSON(&requestSettings)
	if err != nil {
		log.Println("MALFORMED REQ: " + err.Error())
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	var newTheme string
	for _, setting := range requestSettings {
		if setting.Key == "" {
			c.JSON(http.StatusBadRequest, "Missing Key Name for Setting")
			return
		}
		(*a.db).Save(&setting)
		if setting.Key == "theme" {
			newTheme = setting.Value
		}
	}

	// Hot-reload templates if theme changed
	if newTheme != "" && a.OnThemeChange != nil {
		a.OnThemeChange(newTheme)
	}

	c.JSON(http.StatusAccepted, requestSettings)
}

func (a *Admin) GetSetting(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	key := c.Param("key")
	log.Println("Getting setting: ", key)

	err := (*a.db).Where("key = ?", key).First(&blog.Setting{}).Error
	if err != nil {
		c.JSON(http.StatusNotFound, "Setting not found")
	} else {
		c.JSON(http.StatusOK, blog.Setting{})
	}
}

func (a *Admin) GetSettings(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	settingsMap := a.b.GetSettings()
	c.JSON(http.StatusOK, settingsMap)
}

//////HTML API///////

// Admin is the admin dashboard of the website
func (a *Admin) Admin(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin.html", gin.H{
		"posts":      a.b.GetPosts(true),
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}

func (a *Admin) AdminDashboard(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	recentComments := a.b.GetRecentComments(10)
	var postIDs []uint
	for _, comment := range recentComments {
		postIDs = append(postIDs, comment.PostID)
	}
	commentPosts := a.b.GetPostsByIDs(postIDs)
	c.HTML(http.StatusOK, "admin_dashboard.html", gin.H{
		"posts":           a.b.GetPosts(true),
		"logged_in":       a.auth.IsLoggedIn(c),
		"is_admin":        a.auth.IsAdmin(c),
		"version":         a.version,
		"recent":          a.b.GetLatest(),
		"admin_page":      true,
		"settings":        a.b.GetSettings(),
		"recent_comments": recentComments,
		"comment_posts":   commentPosts,
		"nav_pages":       a.b.GetNavPages(),
	})
}

func (a *Admin) AdminPosts(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin_all_posts.html", gin.H{
		"posts":      a.b.GetPosts(true),
		"post_types": a.b.GetPostTypes(),
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}

func (a *Admin) AdminNewPost(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin_new_post.html", gin.H{
		"posts":      a.b.GetPosts(true),
		"post_types": a.b.GetPostTypes(),
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}

func (a *Admin) AdminSettings(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	settings := a.b.GetSettings()
	c.HTML(http.StatusOK, "admin_settings.html", gin.H{
		"posts":          a.b.GetPosts(true),
		"logged_in":      a.auth.IsLoggedIn(c),
		"is_admin":       a.auth.IsAdmin(c),
		"version":        a.version,
		"recent":         a.b.GetLatest(),
		"admin_page":     true,
		"settings":       settings,
		"setting_groups": GroupSettings(settings),
		"nav_pages":      a.b.GetNavPages(),
		"themes":         ListThemes(),
	})
}

// Page CRUD API handlers

var reservedSlugs = map[string]bool{
	"admin": true, "api": true, "login": true, "logout": true,
	"wizard": true, "search": true, "tag": true, "tags": true,
	"archives": true, "sitemap.xml": true, "comments": true,
	"wp-content": true, "index.php": true,
}

var reSlugValid = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func sanitizeSlug(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "/", "")
	return slug
}

func isReservedSlug(slug string) bool {
	return reservedSlugs[slug]
}

// ListPages returns all pages as JSON
func (a *Admin) ListPages(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var pages []blog.Page
	(*a.db).Order("nav_order asc").Find(&pages)
	c.JSON(http.StatusOK, pages)
}

// CreatePage creates a new page
func (a *Admin) CreatePage(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var page blog.Page
	if err := c.BindJSON(&page); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if page.Title == "" {
		c.JSON(http.StatusBadRequest, "Missing Title")
		return
	}

	// An empty page_type used to fall through to the custom-content
	// template; a page type no plugin owns now 404s instead, so keep raw API
	// callers on the old behaviour.
	if page.PageType == "" {
		page.PageType = blog.PageTypeCustom
	}

	page.Slug = sanitizeSlug(page.Slug)
	if page.Slug == "" {
		c.JSON(http.StatusBadRequest, "Missing or invalid Slug")
		return
	}

	if !reSlugValid.MatchString(page.Slug) {
		c.JSON(http.StatusBadRequest, "Slug contains invalid characters")
		return
	}

	if isReservedSlug(page.Slug) {
		c.JSON(http.StatusBadRequest, "Slug '"+page.Slug+"' is reserved")
		return
	}

	// Check uniqueness
	var existing blog.Page
	if err := (*a.db).Where("slug = ?", page.Slug).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, "A page with slug '"+page.Slug+"' already exists")
		return
	}

	// Check conflict with post type slugs
	var existingType blog.PostType
	if err := (*a.db).Where("slug = ?", page.Slug).First(&existingType).Error; err == nil {
		c.JSON(http.StatusConflict, "A post type with slug '"+page.Slug+"' already exists")
		return
	}

	(*a.db).Create(&page)
	c.JSON(http.StatusCreated, page)
}

// UpdatePage updates an existing page
func (a *Admin) UpdatePage(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestPage blog.Page
	if err := c.BindJSON(&requestPage); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if requestPage.ID == 0 {
		c.JSON(http.StatusBadRequest, "Missing page ID")
		return
	}

	var existingPage blog.Page
	if err := (*a.db).Where("id = ?", requestPage.ID).First(&existingPage).Error; err != nil {
		c.JSON(http.StatusNotFound, "Page not found")
		return
	}

	requestPage.Slug = sanitizeSlug(requestPage.Slug)
	if requestPage.Slug == "" {
		c.JSON(http.StatusBadRequest, "Missing or invalid Slug")
		return
	}

	if !reSlugValid.MatchString(requestPage.Slug) {
		c.JSON(http.StatusBadRequest, "Slug contains invalid characters")
		return
	}

	if isReservedSlug(requestPage.Slug) {
		c.JSON(http.StatusBadRequest, "Slug '"+requestPage.Slug+"' is reserved")
		return
	}

	// Check slug uniqueness (excluding current page)
	var conflict blog.Page
	if err := (*a.db).Where("slug = ? AND id != ?", requestPage.Slug, requestPage.ID).First(&conflict).Error; err == nil {
		c.JSON(http.StatusConflict, "A page with slug '"+requestPage.Slug+"' already exists")
		return
	}

	// Check conflict with post type slugs
	var conflictType blog.PostType
	if err := (*a.db).Where("slug = ?", requestPage.Slug).First(&conflictType).Error; err == nil && existingPage.Slug != requestPage.Slug {
		c.JSON(http.StatusConflict, "A post type with slug '"+requestPage.Slug+"' already exists")
		return
	}

	// Update fields
	existingPage.Title = requestPage.Title
	existingPage.Slug = requestPage.Slug
	existingPage.Content = requestPage.Content
	existingPage.HeroURL = requestPage.HeroURL
	existingPage.HeroType = requestPage.HeroType
	existingPage.PageType = requestPage.PageType
	existingPage.NavOrder = requestPage.NavOrder
	existingPage.ScholarID = requestPage.ScholarID
	existingPage.PostTypeID = requestPage.PostTypeID

	(*a.db).Model(&existingPage).Updates(&existingPage)
	// Handle GORM zero-value for nullable pointer
	(*a.db).Model(&existingPage).Select("post_type_id").Update("post_type_id", requestPage.PostTypeID)

	// Handle GORM zero-value booleans
	(*a.db).Model(&existingPage).Select("show_in_nav").Update("show_in_nav", requestPage.ShowInNav)
	(*a.db).Model(&existingPage).Select("enabled").Update("enabled", requestPage.Enabled)
	existingPage.ShowInNav = requestPage.ShowInNav
	existingPage.Enabled = requestPage.Enabled

	c.JSON(http.StatusAccepted, existingPage)
}

// DeletePage deletes a page
func (a *Admin) DeletePage(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}

	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var requestPage blog.Page
	if err := c.BindJSON(&requestPage); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	(*a.db).Where("id = ?", requestPage.ID).Delete(&blog.Page{})
	c.JSON(http.StatusOK, "")
}

// AdminPages renders the admin page listing
func (a *Admin) AdminPages(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var pages []blog.Page
	(*a.db).Order("nav_order asc").Find(&pages)
	c.HTML(http.StatusOK, "admin_pages.html", gin.H{
		"pages":                 pages,
		"logged_in":             a.auth.IsLoggedIn(c),
		"is_admin":              a.auth.IsAdmin(c),
		"version":               a.version,
		"recent":                a.b.GetLatest(),
		"admin_page":            true,
		"settings":              a.b.GetSettings(),
		"nav_pages":             a.b.GetNavPages(),
		"disabled_plugin_pages": a.getDisabledPluginPageTypes(c),
	})
}

const adminCommentsPerPage = 50

// AdminComments renders a paginated list of all comments, newest first
func (a *Admin) AdminComments(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	comments, total := a.b.GetComments((page-1)*adminCommentsPerPage, adminCommentsPerPage)
	totalPages := int((total + adminCommentsPerPage - 1) / adminCommentsPerPage)
	if totalPages < 1 {
		totalPages = 1
	}

	var postIDs []uint
	for _, comment := range comments {
		postIDs = append(postIDs, comment.PostID)
	}
	c.HTML(http.StatusOK, "admin_comments.html", gin.H{
		"comments":      comments,
		"comment_posts": a.b.GetPostsByIDs(postIDs),
		"total":         total,
		"page":          page,
		"total_pages":   totalPages,
		"prev_page":     page - 1,
		"next_page":     page + 1,
		"logged_in":     a.auth.IsLoggedIn(c),
		"is_admin":      a.auth.IsAdmin(c),
		"version":       a.version,
		"recent":        a.b.GetLatest(),
		"admin_page":    true,
		"settings":      a.b.GetSettings(),
		"nav_pages":     a.b.GetNavPages(),
	})
}

const adminUsersPerPage = 50

// AdminUsers renders the user list with promote/demote controls (#548).
func (a *Admin) AdminUsers(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	users, total, err := a.auth.ListUsers((page-1)*adminUsersPerPage, adminUsersPerPage)
	if err != nil {
		log.Println("ERROR LISTING USERS: ", err)
		c.JSON(http.StatusInternalServerError, "Error listing users")
		return
	}
	totalPages := int((total + adminUsersPerPage - 1) / adminUsersPerPage)
	if totalPages < 1 {
		totalPages = 1
	}

	// The last admin can't be demoted (auth.ErrLastAdmin), so don't offer it.
	var adminCount int64
	(*a.db).Model(&auth.AdminUser{}).Count(&adminCount)

	c.HTML(http.StatusOK, "admin_users.html", gin.H{
		"users":       users,
		"can_demote":  adminCount > 1,
		"total":       total,
		"page":        page,
		"total_pages": totalPages,
		"prev_page":   page - 1,
		"next_page":   page + 1,
		"logged_in":   a.auth.IsLoggedIn(c),
		"is_admin":    a.auth.IsAdmin(c),
		"version":     a.version,
		"recent":      a.b.GetLatest(),
		"admin_page":  true,
		"settings":    a.b.GetSettings(),
		"nav_pages":   a.b.GetNavPages(),
	})
}

// adminRequest is the JSON body of the promote/demote endpoints.
type adminRequest struct {
	ID int `json:"id"`
}

// PromoteAdmin makes the user in the request body an admin.
// POST /api/v1/admins {"id": <blog user id>}
func (a *Admin) PromoteAdmin(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	var req adminRequest
	if c.BindJSON(&req) != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, "Malformed request, missing some information")
		return
	}
	switch err := a.auth.PromoteAdmin(req.ID); {
	case err == nil:
		c.JSON(http.StatusOK, "")
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, "No such user")
	case errors.Is(err, auth.ErrNotPromotable):
		c.JSON(http.StatusBadRequest, err.Error())
	default:
		log.Println("ERROR PROMOTING ADMIN: ", err)
		c.JSON(http.StatusInternalServerError, "Error promoting user")
	}
}

// DemoteAdmin removes admin from the user in the request body. Refused with
// 409 when they are the last admin.
// DELETE /api/v1/admins {"id": <blog user id>}
func (a *Admin) DemoteAdmin(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	var req adminRequest
	if c.BindJSON(&req) != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, "Malformed request, missing some information")
		return
	}
	switch err := a.auth.DemoteAdmin(req.ID); {
	case err == nil:
		c.JSON(http.StatusOK, "")
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, "That user is not an admin")
	case errors.Is(err, auth.ErrLastAdmin):
		c.JSON(http.StatusConflict, err.Error())
	default:
		log.Println("ERROR DEMOTING ADMIN: ", err)
		c.JSON(http.StatusInternalServerError, "Error demoting user")
	}
}

// AdminEditPage renders the form to edit a single page
func (a *Admin) AdminEditPage(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error":       "Invalid page ID",
			"description": "Page ID must be a number",
			"version":     a.version,
			"admin_page":  true,
			"settings":    a.b.GetSettings(),
			"nav_pages":   a.b.GetNavPages(),
		})
		return
	}

	var page blog.Page
	if err := (*a.db).Where("id = ?", id).First(&page).Error; err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error":       "Page Not Found",
			"description": "No page with ID " + idStr,
			"version":     a.version,
			"admin_page":  true,
			"settings":    a.b.GetSettings(),
			"nav_pages":   a.b.GetNavPages(),
		})
		return
	}

	c.HTML(http.StatusOK, "admin_edit_page.html", gin.H{
		"page":                  page,
		"post_types":            a.b.GetPostTypes(),
		"logged_in":             a.auth.IsLoggedIn(c),
		"is_admin":              a.auth.IsAdmin(c),
		"version":               a.version,
		"recent":                a.b.GetLatest(),
		"admin_page":            true,
		"settings":              a.b.GetSettings(),
		"nav_pages":             a.b.GetNavPages(),
		"disabled_plugin_pages": a.getDisabledPluginPageTypes(c),
	})
}

// PostType CRUD

// ListPostTypes returns all post types as JSON
func (a *Admin) ListPostTypes(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.JSON(http.StatusOK, a.b.GetPostTypes())
}

// CreatePostType creates a new post type
func (a *Admin) CreatePostType(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var pt blog.PostType
	if err := c.BindJSON(&pt); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if pt.Name == "" {
		c.JSON(http.StatusBadRequest, "Missing Name")
		return
	}

	pt.Slug = sanitizeSlug(pt.Slug)
	if pt.Slug == "" {
		c.JSON(http.StatusBadRequest, "Missing or invalid Slug")
		return
	}
	if !reSlugValid.MatchString(pt.Slug) {
		c.JSON(http.StatusBadRequest, "Slug contains invalid characters")
		return
	}
	if isReservedSlug(pt.Slug) {
		c.JSON(http.StatusBadRequest, "Slug '"+pt.Slug+"' is reserved")
		return
	}

	// Check conflict with existing post type slugs
	var existing blog.PostType
	if err := (*a.db).Where("slug = ?", pt.Slug).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, "A post type with slug '"+pt.Slug+"' already exists")
		return
	}

	// Check conflict with page slugs
	var existingPage blog.Page
	if err := (*a.db).Where("slug = ?", pt.Slug).First(&existingPage).Error; err == nil {
		c.JSON(http.StatusConflict, "A page with slug '"+pt.Slug+"' already exists")
		return
	}

	(*a.db).Create(&pt)
	c.JSON(http.StatusCreated, pt)
}

// UpdatePostType updates an existing post type
func (a *Admin) UpdatePostType(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var req blog.PostType
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}
	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, "Missing post type ID")
		return
	}

	var existing blog.PostType
	if err := (*a.db).Where("id = ?", req.ID).First(&existing).Error; err != nil {
		c.JSON(http.StatusNotFound, "Post type not found")
		return
	}

	if req.Name == "" {
		c.JSON(http.StatusBadRequest, "Missing Name")
		return
	}

	req.Slug = sanitizeSlug(req.Slug)
	if req.Slug == "" {
		c.JSON(http.StatusBadRequest, "Missing or invalid Slug")
		return
	}
	if !reSlugValid.MatchString(req.Slug) {
		c.JSON(http.StatusBadRequest, "Slug contains invalid characters")
		return
	}
	if isReservedSlug(req.Slug) {
		c.JSON(http.StatusBadRequest, "Slug '"+req.Slug+"' is reserved")
		return
	}

	// Check slug uniqueness (excluding current)
	var conflict blog.PostType
	if err := (*a.db).Where("slug = ? AND id != ?", req.Slug, req.ID).First(&conflict).Error; err == nil {
		c.JSON(http.StatusConflict, "A post type with slug '"+req.Slug+"' already exists")
		return
	}

	// Check conflict with page slugs
	var conflictPage blog.Page
	if err := (*a.db).Where("slug = ?", req.Slug).First(&conflictPage).Error; err == nil && existing.Slug != req.Slug {
		c.JSON(http.StatusConflict, "A page with slug '"+req.Slug+"' already exists")
		return
	}

	existing.Name = req.Name
	existing.Slug = req.Slug
	existing.Description = req.Description
	(*a.db).Save(&existing)
	c.JSON(http.StatusAccepted, existing)
}

// DeletePostType deletes a post type if no posts reference it
func (a *Admin) DeletePostType(c *gin.Context) {
	if !strings.HasPrefix(c.ContentType(), "application/json") {
		c.JSON(http.StatusUnsupportedMediaType, "Expecting application/json")
		return
	}
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	var req blog.PostType
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, "Missing post type ID")
		return
	}

	var existing blog.PostType
	if err := (*a.db).Where("id = ?", req.ID).First(&existing).Error; err != nil {
		c.JSON(http.StatusNotFound, "Post type not found")
		return
	}

	// Check if posts reference this type
	var count int64
	(*a.db).Model(&blog.Post{}).Where("post_type_id = ?", req.ID).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, fmt.Sprintf("Cannot delete: %d posts use this type", count))
		return
	}

	(*a.db).Where("id = ?", req.ID).Delete(&blog.PostType{})
	c.JSON(http.StatusOK, "")
}

// AdminPostTypes renders the admin post types listing
func (a *Admin) AdminPostTypes(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	c.HTML(http.StatusOK, "admin_post_types.html", gin.H{
		"post_types": a.b.GetPostTypes(),
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}

// AdminEditPostType renders the form to edit a single post type
func (a *Admin) AdminEditPostType(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error":       "Invalid post type ID",
			"description": "Post type ID must be a number",
			"version":     a.version,
			"admin_page":  true,
			"settings":    a.b.GetSettings(),
			"nav_pages":   a.b.GetNavPages(),
		})
		return
	}

	var pt blog.PostType
	if err := (*a.db).Where("id = ?", id).First(&pt).Error; err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error":       "Post Type Not Found",
			"description": "No post type with ID " + idStr,
			"version":     a.version,
			"admin_page":  true,
			"settings":    a.b.GetSettings(),
			"nav_pages":   a.b.GetNavPages(),
		})
		return
	}

	c.HTML(http.StatusOK, "admin_edit_post_type.html", gin.H{
		"post_type":  pt,
		"logged_in":  a.auth.IsLoggedIn(c),
		"is_admin":   a.auth.IsAdmin(c),
		"version":    a.version,
		"recent":     a.b.GetLatest(),
		"admin_page": true,
		"settings":   a.b.GetSettings(),
		"nav_pages":  a.b.GetNavPages(),
	})
}

func (a *Admin) Post(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		log.Println("IS ADMIN RETURNED FALSE")
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}

	post, err := a.b.GetPostObject(c)
	if err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error":       "Post Not Found",
			"description": err.Error(),
			"version":     a.b.Version,
			"title":       "Post Not Found",
			"recent":      a.b.GetLatest(),
			"admin_page":  true,
			"settings":    a.b.GetSettings(),
			"nav_pages":   a.b.GetNavPages(),
		})
	} else {
		c.HTML(http.StatusOK, "post-admin.html", gin.H{
			"logged_in":          a.auth.IsLoggedIn(c),
			"is_admin":           a.auth.IsAdmin(c),
			"post":               post,
			"post_types":         a.b.GetPostTypes(),
			"version":            a.b.Version,
			"recent":             a.b.GetLatest(),
			"admin_page":         true,
			"settings":           a.b.GetSettings(),
			"backlinks":          a.b.GetBacklinks(post.ID),
			"outbound_links":     a.b.GetOutboundLinks(post.ID),
			"external_backlinks": a.b.GetExternalBacklinks(post.ID),
			"nav_pages":          a.b.GetNavPages(),
		})
	}
}
