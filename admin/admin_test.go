package admin_test

import (
	"bytes"
	"encoding/json"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	tinstaller "goblog/theme/installer"
	"html/template"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Auth struct {
	mock.Mock
	// user is what CurrentUser reports; nil means nobody is logged in.
	user *auth.BlogUser
	real *auth.Auth
}

func (m *Auth) CurrentUser(c *gin.Context) *auth.BlogUser { return m.user }

func (m *Auth) IsAdmin(c *gin.Context) bool {
	args := m.Called(c)
	return args.Bool(0)
}

func (m *Auth) IsLoggedIn(c *gin.Context) bool {
	args := m.Called(c)
	return args.Bool(0)
}

func (m *Auth) IsWizardMode(c *gin.Context) bool {
	args := m.Called(c)
	return args.Bool(0)
}

func (m *Auth) EmailLoginEnabled() bool { return false }

// User management delegates to a real auth.Auth over the test DB (set by
// tests that need it) so the handlers are exercised against real behaviour
// rather than a mock's.
func (m *Auth) ListUsers(offset, limit int) ([]auth.UserListing, int64, error) {
	return m.real.ListUsers(offset, limit)
}
func (m *Auth) PromoteAdmin(userID int) error { return m.real.PromoteAdmin(userID) }
func (m *Auth) DemoteAdmin(userID int) error  { return m.real.DemoteAdmin(userID) }

func TestCreatePost(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{})
	db.AutoMigrate(&blog.PostType{})
	db.AutoMigrate(&blog.Post{})
	db.AutoMigrate(&blog.Tag{})
	db.AutoMigrate(&blog.Comment{})
	db.AutoMigrate(&blog.Page{})
	db.AutoMigrate(&blog.PostRevision{})

	// Seed default post type
	defaultType := blog.PostType{Name: "Post", Slug: "posts", Description: "Blog posts"}
	db.Create(&defaultType)
	a := &Auth{}

	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	router := gin.Default()
	store := cookie.NewStore([]byte("changelater"))
	router.Use(sessions.Sessions("www.jasonernst.com", store))
	router.POST("/api/v1/posts", ad.CreatePost)
	router.GET("/api/v1/posts", b.ListPosts)
	router.PATCH("/api/v1/posts", ad.UpdatePost)
	router.DELETE("/api/v1/posts", ad.DeletePost)
	router.GET("/api/v1/revisions/:id", ad.ListRevisions)
	router.POST("/api/v1/revisions/:id/rollback/:revisionId", ad.RollbackRevision)
	router.DELETE("/api/v1/comments", ad.DeleteComment)
	router.POST("/api/v1/upload", ad.UploadFile)

	router.GET("/api/v1/pages", ad.ListPages)
	router.POST("/api/v1/pages", ad.CreatePage)
	router.PATCH("/api/v1/pages", ad.UpdatePage)
	router.DELETE("/api/v1/pages", ad.DeletePage)

	router.GET("/api/v1/post-types", ad.ListPostTypes)
	router.POST("/api/v1/post-types", ad.CreatePostType)
	router.PATCH("/api/v1/post-types", ad.UpdatePostType)
	router.DELETE("/api/v1/post-types", ad.DeletePostType)

	router.GET("/admin", ad.Admin)
	router.GET("/admin/dashboard", ad.AdminDashboard)
	router.GET("/admin/posts", ad.AdminPosts)
	router.GET("/admin/newpost", ad.AdminNewPost)
	router.GET("/admin/settings", ad.AdminSettings)
	router.GET("/admin/pages", ad.AdminPages)
	router.GET("/admin/pages/:id", ad.AdminEditPage)
	router.GET("/admin/post-types", ad.AdminPostTypes)
	router.GET("/admin/post-types/:id", ad.AdminEditPostType)

	//improper content-type
	testPost := blog.Post{
		Title:   "Test title",
		Content: "This is some test content",
	}
	jsonValue, _ := json.Marshal(testPost)
	req, _ := http.NewRequest("POST", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnsupportedMediaType, w.Code)
	}

	//is not admin
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req.Header.Add("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnauthorized, w.Code)
	}

	//is admin
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusCreated, w.Code)
	}

	//missing title
	testPost = blog.Post{
		Content: "This is some test content",
	}
	jsonValue, _ = json.Marshal(testPost)
	req, _ = http.NewRequest("POST", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//list all posts, should not be empty
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), testPost.Title) {
		t.Errorf("Expected to see a post with title: %s but didn't", testPost.Title)
	}

	//get specific post
	var posts []blog.Post
	err := json.Unmarshal(w.Body.Bytes(), &posts)
	if err != nil {
		t.Errorf("Couldn't parse the posts")
	}
	post := posts[0]
	jsonValue, _ = json.Marshal(post)
	req, _ = http.NewRequest("GET", "/api/v1/posts/"+strconv.Itoa(post.CreatedAt.Year())+"/"+strconv.Itoa(int(post.CreatedAt.Month()))+"/"+strconv.Itoa(post.CreatedAt.Day())+"/"+post.Slug, bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), testPost.Title) {
		t.Errorf("Expected to see a post with title: %s but didn't", testPost.Title)
	}

	//update post
	testPost = blog.Post{
		Title:   "Test title updated",
		Content: "This is some test content updated",
	}
	testPost.ID = post.ID
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusAccepted, w.Code)
	}

	// update post with tags, then save again — tags should not double
	testPost = blog.Post{
		Title:   "Test title with tags",
		Content: "Content with tags",
		Tags:    []blog.Tag{{Name: "go"}, {Name: "web"}},
	}
	testPost.ID = post.ID
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d but got %d\n", http.StatusAccepted, w.Code)
	}
	var updatedPost blog.Post
	if err := json.Unmarshal(w.Body.Bytes(), &updatedPost); err != nil {
		t.Fatalf("Failed to unmarshal updated post response: %v", err)
	}
	// reload with tags
	db.Preload("Tags").First(&updatedPost, post.ID)
	if len(updatedPost.Tags) != 2 {
		t.Fatalf("Expected 2 tags after first save, got %d", len(updatedPost.Tags))
	}

	// save again with same tags — should still be 2, not 4
	testPost.Tags = []blog.Tag{{Name: "go"}, {Name: "web"}}
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d but got %d\n", http.StatusAccepted, w.Code)
	}
	db.Preload("Tags").First(&updatedPost, post.ID)
	if len(updatedPost.Tags) != 2 {
		t.Fatalf("Expected 2 tags after second save (no duplication), got %d", len(updatedPost.Tags))
	}

	// --- Revision history tests ---

	// List revisions — should have revisions from the updates above
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/revisions/"+strconv.Itoa(int(post.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for list revisions but got %d", http.StatusOK, w.Code)
	}
	var revisions []blog.PostRevision
	if err := json.Unmarshal(w.Body.Bytes(), &revisions); err != nil {
		t.Fatalf("Failed to unmarshal revisions: %v", err)
	}
	if len(revisions) == 0 {
		t.Fatal("Expected at least one revision after updates")
	}
	firstRevision := revisions[len(revisions)-1] // oldest revision

	// List revisions — not admin → 401
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/revisions/"+strconv.Itoa(int(post.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected status %d for non-admin list revisions but got %d", http.StatusUnauthorized, w.Code)
	}

	// Rollback to first revision
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/revisions/"+strconv.Itoa(int(post.ID))+"/rollback/"+strconv.Itoa(int(firstRevision.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d for rollback but got %d", http.StatusAccepted, w.Code)
	}
	var rolledBack blog.Post
	if err := json.Unmarshal(w.Body.Bytes(), &rolledBack); err != nil {
		t.Fatalf("Failed to unmarshal rollback response: %v", err)
	}
	if rolledBack.Title != firstRevision.Title {
		t.Fatalf("Expected title %q after rollback, got %q", firstRevision.Title, rolledBack.Title)
	}

	// Rollback — not admin → 401
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/revisions/"+strconv.Itoa(int(post.ID))+"/rollback/"+strconv.Itoa(int(firstRevision.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected status %d for non-admin rollback but got %d", http.StatusUnauthorized, w.Code)
	}

	// Rollback — bad revision ID → 404
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/revisions/"+strconv.Itoa(int(post.ID))+"/rollback/99999", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected status %d for bad revision rollback but got %d", http.StatusNotFound, w.Code)
	}

	//update post, bad type
	jsonValue, _ = json.Marshal(testPost)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnsupportedMediaType, w.Code)
	}

	//update post, not admin
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnauthorized, w.Code)
	}

	//missing Title
	testPost = blog.Post{
		Title:   "",
		Content: "This is some test content updated",
	}
	testPost.ID = post.ID
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//missing id
	testPost = blog.Post{
		Title:   "Test",
		Content: "This is some test content updated",
	}
	testPost.ID = 99999
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//delete post: incorrect content type
	jsonValue, _ = json.Marshal(testPost)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnsupportedMediaType, w.Code)
	}

	//delete post: not admin
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusUnauthorized, w.Code)
	}

	//good Delete
	jsonValue, _ = json.Marshal(testPost)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}

	//upload file test
	//https://www.programmersought.com/article/6833575288/
	path := "../README.md"
	file, err := os.Open(path)
	if err != nil {
		t.Error(err)
	}

	defer file.Close()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		writer.Close()
		t.Error(err)
	}
	io.Copy(part, file)
	writer.Close()

	req = httptest.NewRequest("POST", "/api/v1/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w = httptest.NewRecorder()
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	admin.WWWFolder = "../www/"
	admin.UploadsFolder = "uploads/"
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		body, _ := ioutil.ReadAll(w.Body)
		t.Fatalf("Expected to get status %d but instead got %d\n%s", http.StatusOK, w.Code, body)
	}
	err = os.Remove("../uploads/README.md")

	//file upload, upload folder doesn't exist
	//admin.UploadsFolder = "dfadf/"
	//w = httptest.NewRecorder()
	//a.On("IsAdmin", mock.Anything).Return(true).Once()
	//router.ServeHTTP(w, req)
	//if w.Code != http.StatusBadRequest {
	//	body, _ := ioutil.ReadAll(w.Body)
	//	t.Fatalf("Expected to get status %d but instead got %d\n%s", http.StatusBadRequest, w.Code, body)
	//}

	//file upload, not admin and not wizard mode
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsWizardMode", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		body, _ := ioutil.ReadAll(w.Body)
		t.Fatalf("Expected to get status %d but instead got %d\n%s", http.StatusUnauthorized, w.Code, body)
	}

	//file upload, not admin but in wizard mode (fresh install) — should be allowed
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsWizardMode", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/upload", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		// expecting 400 (no file in form) rather than 401 — proves the auth gate let us through
		body, _ := ioutil.ReadAll(w.Body)
		t.Fatalf("Expected to get status %d (past auth gate, missing file) but instead got %d\n%s", http.StatusBadRequest, w.Code, body)
	}

	//file upload, missing file
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/v1/upload", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		body, _ := ioutil.ReadAll(w.Body)
		t.Fatalf("Expected to get status %d but instead got %d\n%s", http.StatusBadRequest, w.Code, body)
	}

	//get admin: /admin is the dashboard's front door and redirects there
	router.SetFuncMap(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	})
	router.LoadHTMLGlob("../themes/default/templates/*")
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	req, _ = http.NewRequest("GET", "/admin", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/admin/dashboard" {
		t.Fatalf("Expected a %d redirect to /admin/dashboard but got %d %q\n", http.StatusFound, w.Code, w.Header().Get("Location"))
	}

	//get admin: not admin -> same 401 as every other admin page, no redirect
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/admin", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected to get status %d for non-admin /admin but instead got %d\n", http.StatusUnauthorized, w.Code)
	}

	// Create a comment to test deletion
	comment := blog.Comment{PostID: post.ID, Name: "Tester", Content: "A test comment"}
	db.Create(&comment)

	// Delete comment: not admin -> 401
	commentJSON, _ := json.Marshal(comment)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/comments", bytes.NewBuffer(commentJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected status %d for non-admin delete comment but got %d", http.StatusUnauthorized, w.Code)
	}

	// Delete comment: admin -> 200
	commentJSON, _ = json.Marshal(comment)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/comments", bytes.NewBuffer(commentJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for admin delete comment but got %d", http.StatusOK, w.Code)
	}

	// --- Page CRUD tests ---

	// Create page: not admin -> 401
	testPage := blog.Page{Title: "Portfolio", Slug: "portfolio", PageType: "custom", Enabled: true, ShowInNav: true, NavOrder: 5}
	pageJSON, _ := json.Marshal(testPage)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected status %d for non-admin create page but got %d", http.StatusUnauthorized, w.Code)
	}

	// Create page: admin -> 201
	pageJSON, _ = json.Marshal(testPage)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected status %d for create page but got %d. Body: %s", http.StatusCreated, w.Code, w.Body.String())
	}

	var createdPage blog.Page
	json.Unmarshal(w.Body.Bytes(), &createdPage)

	// Create page with reserved slug -> 400
	reservedPage := blog.Page{Title: "Admin", Slug: "admin", PageType: "custom"}
	pageJSON, _ = json.Marshal(reservedPage)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected status %d for reserved slug but got %d", http.StatusBadRequest, w.Code)
	}

	// Create page with duplicate slug -> 409
	dupPage := blog.Page{Title: "Portfolio 2", Slug: "portfolio", PageType: "custom"}
	pageJSON, _ = json.Marshal(dupPage)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("Expected status %d for duplicate slug but got %d", http.StatusConflict, w.Code)
	}

	// Create page without a page_type -> stored as "custom", so raw API
	// callers keep the custom-content page instead of a page type no plugin
	// owns (which would 404 as "plugin is not installed").
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/pages", bytes.NewBuffer([]byte(`{"title":"Untyped","slug":"untyped","enabled":true}`)))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected status %d for create page without page_type but got %d. Body: %s", http.StatusCreated, w.Code, w.Body.String())
	}
	var untyped blog.Page
	json.Unmarshal(w.Body.Bytes(), &untyped)
	if untyped.PageType != blog.PageTypeCustom {
		t.Errorf("Expected an empty page_type to default to %q, got %q", blog.PageTypeCustom, untyped.PageType)
	}
	var untypedRow blog.Page
	db.Where("slug = ?", "untyped").First(&untypedRow)
	if untypedRow.PageType != blog.PageTypeCustom {
		t.Errorf("Expected the stored page_type to be %q, got %q", blog.PageTypeCustom, untypedRow.PageType)
	}

	// Update page
	createdPage.Title = "Portfolio Updated"
	pageJSON, _ = json.Marshal(createdPage)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d for update page but got %d. Body: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	// List pages
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/pages", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for list pages but got %d", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), "Portfolio Updated") {
		t.Errorf("Expected list pages to contain updated title")
	}

	// Admin pages HTML (IsAdmin called twice: auth check + template data)
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	a.On("IsLoggedIn", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/admin/pages", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for admin pages but got %d", http.StatusOK, w.Code)
	}

	// Admin edit page HTML (IsAdmin called twice: auth check + template data)
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	a.On("IsLoggedIn", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/admin/pages/"+strconv.Itoa(int(createdPage.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for admin edit page but got %d", http.StatusOK, w.Code)
	}

	// Delete page
	pageJSON, _ = json.Marshal(createdPage)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/pages", bytes.NewBuffer(pageJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for delete page but got %d", http.StatusOK, w.Code)
	}

	// --- Post Type CRUD tests ---

	// Create post type: admin -> 201
	newType := blog.PostType{Name: "Notes", Slug: "notes", Description: "Short notes"}
	typeJSON, _ := json.Marshal(newType)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/post-types", bytes.NewBuffer(typeJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected status %d for create post type but got %d. Body: %s", http.StatusCreated, w.Code, w.Body.String())
	}

	var createdType blog.PostType
	json.Unmarshal(w.Body.Bytes(), &createdType)

	// Create post type with reserved slug -> 400
	reservedType := blog.PostType{Name: "Admin", Slug: "admin"}
	typeJSON, _ = json.Marshal(reservedType)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/post-types", bytes.NewBuffer(typeJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected status %d for reserved slug but got %d", http.StatusBadRequest, w.Code)
	}

	// Create post type with duplicate slug -> 409
	dupType := blog.PostType{Name: "Notes 2", Slug: "notes"}
	typeJSON, _ = json.Marshal(dupType)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("POST", "/api/v1/post-types", bytes.NewBuffer(typeJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("Expected status %d for duplicate slug but got %d", http.StatusConflict, w.Code)
	}

	// Update post type
	createdType.Name = "Notes Updated"
	typeJSON, _ = json.Marshal(createdType)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/v1/post-types", bytes.NewBuffer(typeJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d for update post type but got %d. Body: %s", http.StatusAccepted, w.Code, w.Body.String())
	}

	// List post types
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/post-types", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for list post types but got %d", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), "Notes Updated") {
		t.Errorf("Expected list post types to contain updated name")
	}

	// Admin post types HTML
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	a.On("IsLoggedIn", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/admin/post-types", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for admin post types but got %d", http.StatusOK, w.Code)
	}

	// Admin edit post type HTML
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	a.On("IsLoggedIn", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/admin/post-types/"+strconv.Itoa(int(createdType.ID)), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for admin edit post type but got %d", http.StatusOK, w.Code)
	}

	// Delete post type
	typeJSON, _ = json.Marshal(createdType)
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/v1/post-types", bytes.NewBuffer(typeJSON))
	req.Header.Add("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected status %d for delete post type but got %d", http.StatusOK, w.Code)
	}
}

// TestAdminComments covers the paginated admin comments page (issue #545).
func TestAdminComments(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	router := gin.Default()
	store := cookie.NewStore([]byte("changelater"))
	router.Use(sessions.Sessions("www.jasonernst.com", store))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/admin/comments", ad.AdminComments)

	post := blog.Post{Title: "Commented Post", Content: "body", Slug: "commented-post"}
	db.Create(&post)
	for i := 1; i <= 51; i++ {
		db.Create(&blog.Comment{PostID: post.ID, Name: "Commenter" + strconv.Itoa(i), Email: "c@example.com", Content: "Comment number " + strconv.Itoa(i)})
	}

	// Non-admin -> 401
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/comments", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d for non-admin, got %d", http.StatusUnauthorized, w.Code)
	}

	get := func(path string) string {
		a.On("IsAdmin", mock.Anything).Return(true).Twice()
		a.On("IsLoggedIn", mock.Anything).Return(true).Once()
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		return w.Body.String()
	}

	// Page 1: 50 newest comments, link to the post, delete control, next link, no prev link
	body := get("/admin/comments")
	if strings.Count(body, `id="comment-row-`) != 50 {
		t.Errorf("expected 50 comment rows on page 1, got %d", strings.Count(body, `id="comment-row-`))
	}
	if !strings.Contains(body, "Commenter51") || strings.Contains(body, "Commenter1<") {
		t.Errorf("expected page 1 to contain the newest comment and not the oldest")
	}
	if !strings.Contains(body, post.Permalink()) || !strings.Contains(body, "Commented Post") {
		t.Errorf("expected comment rows to link to the post")
	}
	if !strings.Contains(body, "deleteComment(") {
		t.Errorf("expected delete controls on comment rows")
	}
	if !strings.Contains(body, "/admin/comments?page=2") {
		t.Errorf("expected a link to page 2")
	}
	if strings.Contains(body, "/admin/comments?page=0") {
		t.Errorf("did not expect a link to a previous page on page 1")
	}
	if !strings.Contains(body, "Page 1 of 2") || !strings.Contains(body, "51 comments") {
		t.Errorf("expected pagination summary 'Page 1 of 2' and '51 comments'")
	}

	// Page 2: the one remaining (oldest) comment, prev link, no next link
	body = get("/admin/comments?page=2")
	if strings.Count(body, `id="comment-row-`) != 1 || !strings.Contains(body, "Commenter1<") {
		t.Errorf("expected only the oldest comment on page 2")
	}
	if !strings.Contains(body, "/admin/comments?page=1") || strings.Contains(body, "/admin/comments?page=3") {
		t.Errorf("expected a prev link and no next link on the last page")
	}

	// Invalid page values fall back to page 1
	for _, p := range []string{"abc", "0", "-3"} {
		body = get("/admin/comments?page=" + p)
		if !strings.Contains(body, "Page 1 of 2") {
			t.Errorf("page=%s: expected fallback to page 1", p)
		}
	}
}

// TestAdminSettings_RendersCheckboxSetting checks a "checkbox" typed setting
// (comments_require_login, issue #524) renders as a checkbox reflecting its
// value in every theme, rather than a required text input.
func TestAdminSettings_RendersCheckboxSetting(t *testing.T) {
	for _, theme := range []string{"default", overlayTheme} {
		for _, value := range []string{"true", "false"} {
			t.Run(theme+"/"+value, func(t *testing.T) {
				db, _ := gorm.Open(sqlite.Open(":memory:"))
				db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Setting{}, &blog.Page{})
				db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: value})
				a := &Auth{}
				b := blog.New(db, a, "test")
				ad := admin.New(db, a, &b, "test")

				gin.SetMode(gin.TestMode)
				router := gin.New()
				router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
				router.SetHTMLTemplate(themeTemplates(t, theme))
				router.GET("/admin/settings", ad.AdminSettings)

				a.On("IsAdmin", mock.Anything).Return(true)
				a.On("IsLoggedIn", mock.Anything).Return(true)
				w := httptest.NewRecorder()
				req, _ := http.NewRequest("GET", "/admin/settings", nil)
				router.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("expected 200, got %d", w.Code)
				}
				body := w.Body.String()
				re := regexp.MustCompile(`<input[^>]*type="checkbox"[^>]*name="comments_require_login"[^>]*>`)
				m := re.FindString(body)
				if m == "" {
					t.Fatalf("expected a checkbox input for comments_require_login, body: %.300s", body)
				}
				if checked := strings.Contains(m, "checked"); checked != (value == "true") {
					t.Errorf("value %s: checked=%v in %s", value, checked, m)
				}
				if strings.Contains(m, "required") {
					t.Errorf("checkbox must not be required: %s", m)
				}
			})
		}
	}
}

// newSettingsPage seeds the given settings, renders /admin/settings with the
// default theme and returns the status and body.
func newSettingsPage(t *testing.T, isAdmin bool, rows ...blog.Setting) (int, string) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Setting{}, &blog.Page{})
	for _, s := range rows {
		db.Create(&s)
	}
	a := &Auth{}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/admin/settings", ad.AdminSettings)

	a.On("IsAdmin", mock.Anything).Return(isAdmin)
	a.On("IsLoggedIn", mock.Anything).Return(isAdmin)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/settings", nil)
	router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestAdminSettings_NonAdmin_Unauthorized checks the Settings page is gated
// like every other admin page.
func TestAdminSettings_NonAdmin_Unauthorized(t *testing.T) {
	if code, _ := newSettingsPage(t, false); code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

// TestAdminSettings_GroupsEverySeededKey checks the Settings page draws one
// tab per group, puts every seeded key's input inside one form under a
// human label (so Save Settings, which walks #settings-form, sends them
// all), keeps the theme <select> with its Themes hint, and has no plugin
// cards any more.
func TestAdminSettings_GroupsEverySeededKey(t *testing.T) {
	var rows []blog.Setting
	for k, typ := range seededSettings {
		value := "v-" + k
		if k == "theme" {
			value = "default" // must be a real theme to be the selected <option>
		}
		rows = append(rows, blog.Setting{Key: k, Type: typ, Value: value})
	}
	code, body := newSettingsPage(t, true, rows...)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	// Past the header (which has the search form) the page is the settings.
	if i := strings.Index(body, `class="admin-panel"`); i < 0 {
		t.Fatalf("page missing the .admin-panel wrapper")
	} else {
		body = body[i:]
	}
	for _, id := range []string{"site", "appearance", "comments", "directories"} {
		if !strings.Contains(body, `id="tab-`+id+`"`) || !strings.Contains(body, `id="pane-`+id+`"`) {
			t.Errorf("page missing tab %s", id)
		}
	}
	if strings.Contains(body, `id="tab-advanced"`) {
		t.Errorf("page has an Advanced tab with nothing to put in it")
	}
	if n := strings.Count(body, "<form"); n != 1 {
		t.Errorf("expected one form spanning the tabs, found %d", n)
	}
	for key := range seededSettings {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("page missing an input for %s", key)
		}
		if strings.Contains(body, `class="form-label">`+key+`<`) {
			t.Errorf("%s is labelled by its raw key", key)
		}
	}
	for _, want := range []string{`<select id="theme" name="theme"`, `<option value="default" selected>`, `href="/admin/themes"`, `Plugin settings live on each plugin`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, gone := range []string{"plugin-body-", "plugin-settings-form", "updatePluginSettings"} {
		if strings.Contains(body, gone) {
			t.Errorf("page still has plugin settings markup %q", gone)
		}
	}
	if m := regexp.MustCompile(`class="h[1-6]"`).FindString(body); m != "" {
		t.Errorf("page uses Tachyons-clashing %s", m)
	}
}

// TestAdminSettings_UnknownKeyInAdvanced checks a setting the layout does
// not know still gets an input, under an Advanced tab labelled by its key.
func TestAdminSettings_UnknownKeyInAdvanced(t *testing.T) {
	code, body := newSettingsPage(t, true,
		blog.Setting{Key: "site_title", Type: "text", Value: "T"},
		blog.Setting{Key: "some_new_thing", Type: "text", Value: "x"},
	)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !strings.Contains(body, `id="tab-advanced"`) {
		t.Fatalf("page missing the Advanced tab")
	}
	adv := body[strings.Index(body, `id="pane-advanced"`):]
	if !strings.Contains(adv, `for="some_new_thing" class="form-label">some_new_thing<`) || !strings.Contains(adv, `name="some_new_thing" value="x"`) {
		t.Errorf("Advanced pane missing the unknown key's input: %.600s", adv)
	}
}

// newUsersHarness wires an Admin with a real auth.Auth for user management
// and the given theme's templates, returning the router, the mock auth and
// the DB.
func newUsersHarness(t *testing.T, theme string) (*gin.Engine, *Auth, *gorm.DB) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &auth.AdminUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{})
	realAuth := auth.New(db, "test")
	a := &Auth{real: &realAuth}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
	router.SetHTMLTemplate(themeTemplates(t, theme))
	router.GET("/admin/users", ad.AdminUsers)
	router.POST("/api/v1/admins", ad.PromoteAdmin)
	router.DELETE("/api/v1/admins", ad.DemoteAdmin)
	return router, a, db
}

func seedUser(t *testing.T, db *gorm.DB, provider, id, login string, isAdmin bool) auth.BlogUser {
	t.Helper()
	u := auth.BlogUser{Provider: provider, ProviderID: id, Login: login}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if isAdmin {
		if err := db.Create(&auth.AdminUser{BlogUserID: u.ID}).Error; err != nil {
			t.Fatalf("create admin: %v", err)
		}
	}
	return u
}

func adminsJSON(router *gin.Engine, method string, id int) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]int{"id": id})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, "/api/v1/admins", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestAdminUsers_NonAdmin_Unauthorized(t *testing.T) {
	router, a, _ := newUsersHarness(t, "default")
	a.On("IsAdmin", mock.Anything).Return(false)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/users", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /admin/users: expected 401, got %d", w.Code)
	}
	if w := adminsJSON(router, "POST", 1); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/admins: expected 401, got %d", w.Code)
	}
	if w := adminsJSON(router, "DELETE", 1); w.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE /api/v1/admins: expected 401, got %d", w.Code)
	}
}

// TestAdminUsers_RendersUsers checks every theme lists users with their
// admin status and the right control: promote for GitHub non-admins, demote
// for admins, and nothing for email users (#565) or the last admin.
func TestAdminUsers_RendersUsers(t *testing.T) {
	for _, theme := range []string{"default", overlayTheme} {
		t.Run(theme, func(t *testing.T) {
			router, a, db := newUsersHarness(t, theme)
			boss := seedUser(t, db, auth.ProviderGitHub, "1", "boss", true)
			plain := seedUser(t, db, auth.ProviderGitHub, "2", "plain", false)
			mailer := seedUser(t, db, auth.ProviderEmail, "m@example.com", "m@example.com", false)
			a.On("IsAdmin", mock.Anything).Return(true)
			a.On("IsLoggedIn", mock.Anything).Return(true)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/admin/users", nil)
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			body := w.Body.String()

			row := func(u auth.BlogUser) string {
				re := regexp.MustCompile(`(?s)<tr id="user-row-` + strconv.Itoa(u.ID) + `">.*?</tr>`)
				m := re.FindString(body)
				if m == "" {
					t.Fatalf("no row for %s", u.Login)
				}
				return m
			}
			// html/template renders the JS-context id as "( 2 )"; match loosely.
			control := func(fn string, u auth.BlogUser) *regexp.Regexp {
				return regexp.MustCompile(fn + `\(\s*` + strconv.Itoa(u.ID) + `\s*\)`)
			}
			if r := row(boss); !strings.Contains(r, "Admin") || strings.Contains(r, "demoteAdmin(") || strings.Contains(r, "promoteAdmin(") {
				t.Errorf("last admin must be badged with no controls: %s", r)
			}
			if r := row(plain); !control("promoteAdmin", plain).MatchString(r) {
				t.Errorf("github non-admin must have a promote control: %s", r)
			}
			if r := row(mailer); strings.Contains(r, "promoteAdmin(") || strings.Contains(r, "demoteAdmin(") {
				t.Errorf("email user must have no controls: %s", r)
			}
			if !strings.Contains(body, "3 users") {
				t.Errorf("expected a total in the summary")
			}

			// With a second admin the first one becomes demotable.
			db.Create(&auth.AdminUser{BlogUserID: plain.ID})
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)
			body = w.Body.String()
			if r := row(boss); !control("demoteAdmin", boss).MatchString(r) {
				t.Errorf("admin must have a demote control once another admin exists: %s", r)
			}
		})
	}
}

func TestAdminUsers_Pagination(t *testing.T) {
	router, a, db := newUsersHarness(t, "default")
	seedUser(t, db, auth.ProviderGitHub, "1", "boss", true)
	for i := 2; i <= 51; i++ {
		seedUser(t, db, auth.ProviderGitHub, strconv.Itoa(i), "user"+strconv.Itoa(i), false)
	}
	a.On("IsAdmin", mock.Anything).Return(true)
	a.On("IsLoggedIn", mock.Anything).Return(true)

	get := func(path string) string {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		return w.Body.String()
	}
	body := get("/admin/users")
	if n := strings.Count(body, `id="user-row-`); n != 50 {
		t.Errorf("expected 50 rows on page 1, got %d", n)
	}
	if !strings.Contains(body, "user51") || strings.Contains(body, ">boss<") {
		t.Errorf("page 1 must be newest first and exclude the oldest user")
	}
	if !strings.Contains(body, "Page 1 of 2") || !strings.Contains(body, "/admin/users?page=2") {
		t.Errorf("expected pagination summary and next link")
	}
	body = get("/admin/users?page=2")
	if n := strings.Count(body, `id="user-row-`); n != 1 || !strings.Contains(body, ">boss<") {
		t.Errorf("expected only the oldest user on page 2")
	}
	if !strings.Contains(get("/admin/users?page=abc"), "Page 1 of 2") {
		t.Errorf("invalid page must fall back to page 1")
	}
}

func TestPromoteAdminAPI(t *testing.T) {
	router, a, db := newUsersHarness(t, "default")
	seedUser(t, db, auth.ProviderGitHub, "1", "boss", true)
	plain := seedUser(t, db, auth.ProviderGitHub, "2", "plain", false)
	mailer := seedUser(t, db, auth.ProviderEmail, "m@example.com", "m@example.com", false)
	a.On("IsAdmin", mock.Anything).Return(true)

	if w := adminsJSON(router, "POST", plain.ID); w.Code != http.StatusOK {
		t.Fatalf("promote github user: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&auth.AdminUser{}).Where("blog_user_id = ?", plain.ID).Count(&n)
	if n != 1 {
		t.Fatalf("expected plain to be admin after promotion")
	}
	if w := adminsJSON(router, "POST", mailer.ID); w.Code != http.StatusBadRequest {
		t.Errorf("promote email user: expected 400, got %d", w.Code)
	}
	if w := adminsJSON(router, "POST", 999); w.Code != http.StatusNotFound {
		t.Errorf("promote unknown user: expected 404, got %d", w.Code)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/admins", strings.NewReader("not json"))
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: expected 400, got %d", w.Code)
	}
	// A missing or non-positive id is a malformed request, not an unknown user.
	for _, body := range []string{"{}", `{"id": 0}`, `{"id": -1}`} {
		for _, method := range []string{"POST", "DELETE"} {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(method, "/api/v1/admins", strings.NewReader(body))
			router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s %s: expected 400, got %d", method, body, w.Code)
			}
		}
	}
}

func TestAdminUsers_SingularSummary(t *testing.T) {
	router, a, db := newUsersHarness(t, "default")
	seedUser(t, db, auth.ProviderGitHub, "1", "only", true)
	a.On("IsAdmin", mock.Anything).Return(true)
	a.On("IsLoggedIn", mock.Anything).Return(true)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/users", nil)
	router.ServeHTTP(w, req)
	if body := w.Body.String(); !strings.Contains(body, "1 user<") || strings.Contains(body, "1 users") {
		t.Errorf("expected the summary to read '1 user', body: %.400s", body)
	}
}

// TestAdminUsers_ListError checks a failing user query surfaces as a 500
// rather than rendering an empty list as if there were no users.
func TestAdminUsers_ListError(t *testing.T) {
	router, a, db := newUsersHarness(t, "default")
	a.On("IsAdmin", mock.Anything).Return(true)
	a.On("IsLoggedIn", mock.Anything).Return(true)
	if err := db.Migrator().DropTable(&auth.BlogUser{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/admin/users", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestDemoteAdminAPI(t *testing.T) {
	router, a, db := newUsersHarness(t, "default")
	boss := seedUser(t, db, auth.ProviderGitHub, "1", "boss", true)
	second := seedUser(t, db, auth.ProviderGitHub, "2", "second", true)
	plain := seedUser(t, db, auth.ProviderGitHub, "3", "plain", false)
	a.On("IsAdmin", mock.Anything).Return(true)

	if w := adminsJSON(router, "DELETE", plain.ID); w.Code != http.StatusNotFound {
		t.Errorf("demote non-admin: expected 404, got %d", w.Code)
	}
	if w := adminsJSON(router, "DELETE", second.ID); w.Code != http.StatusOK {
		t.Fatalf("demote with another admin left: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := adminsJSON(router, "DELETE", boss.ID); w.Code != http.StatusConflict {
		t.Errorf("demote last admin: expected 409, got %d", w.Code)
	}
	var n int64
	db.Model(&auth.AdminUser{}).Count(&n)
	if n != 1 {
		t.Fatalf("expected exactly one admin to remain, got %d", n)
	}
}

// overlayTheme is the second theme the admin tests render under: like a
// directory theme it ships only public templates (here just home.html), so
// every admin page must come from default's copies.
const overlayTheme = "overlay"

// themeTemplates parses the templates the way theme.Load layers them: the
// shared set, then default, then the named theme on top — so a theme that
// ships no admin templates renders default's.
func themeTemplates(t *testing.T, theme string) *template.Template {
	t.Helper()
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	if theme != "default" {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("<p>overlay home</p>"), 0o644); err != nil {
			t.Fatal(err)
		}
		template.Must(tmpl.ParseGlob(filepath.Join(dir, "*.html")))
	}
	return tmpl
}

// newAdminHarness wires every admin HTML route over a fresh sqlite DB with a
// real auth.Auth for user management and the given theme's templates.
func newAdminHarness(t *testing.T, theme string) (*gin.Engine, *Auth, *gorm.DB, *admin.Admin) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &auth.AdminUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{})
	realAuth := auth.New(db, "test")
	a := &Auth{real: &realAuth}
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "v9.9.9-test")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
	router.SetHTMLTemplate(themeTemplates(t, theme))
	router.GET("/admin", ad.Admin)
	router.GET("/admin/dashboard", ad.AdminDashboard)
	router.GET("/admin/posts", ad.AdminPosts)
	router.GET("/admin/newpost", ad.AdminNewPost)
	router.GET("/admin/pages", ad.AdminPages)
	router.GET("/admin/pages/:id", ad.AdminEditPage)
	router.GET("/admin/comments", ad.AdminComments)
	router.GET("/admin/users", ad.AdminUsers)
	router.GET("/admin/post-types", ad.AdminPostTypes)
	router.GET("/admin/post-types/:id", ad.AdminEditPostType)
	router.GET("/admin/plugins", ad.AdminPlugins)
	router.GET("/admin/themes", ad.AdminThemes)
	router.GET("/admin/posts/:yyyy/:mm/:dd/:slug", ad.Post)
	return router, a, db, &ad
}

func getHTML(t *testing.T, router *gin.Engine, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", path, nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %.300s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// TestAdminDashboard checks the dashboard summary (#596): count tiles that
// link to their admin pages, the newest posts with draft badges linking to
// their admin edit page, recent comments, and the Site panel.
func TestAdminDashboard(t *testing.T) {
	router, a, db, _ := newAdminHarness(t, "default")
	a.On("IsAdmin", mock.Anything).Return(true)
	a.On("IsLoggedIn", mock.Anything).Return(true)

	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 7; i++ {
		db.Create(&blog.Post{Title: "Dash Post " + strconv.Itoa(i), Slug: "dash-post-" + strconv.Itoa(i), Content: "x", Draft: i == 7, PostTypeID: pt.ID, CreatedAt: base.Add(time.Duration(i) * time.Hour)})
	}
	db.Create(&blog.Page{Title: "About", Slug: "about", Enabled: true})
	db.Create(&blog.Comment{PostID: 1, Name: "Commenter", Content: "Nice post"})
	seedUser(t, db, auth.ProviderGitHub, "1", "boss", true)
	seedUser(t, db, auth.ProviderGitHub, "2", "plain", false)
	db.Create(&blog.Setting{Key: "theme", Value: overlayTheme})

	body := getHTML(t, router, "/admin/dashboard")

	tile := func(id string) string {
		re := regexp.MustCompile(`(?s)<a[^>]*id="tile-` + id + `"[^>]*>.*?</a>`)
		m := re.FindString(body)
		if m == "" {
			t.Fatalf("no %s tile", id)
		}
		return m
	}
	if m := tile("posts"); !strings.Contains(m, `href="/admin/posts"`) || !strings.Contains(m, ">6<") || !strings.Contains(m, "1 draft") {
		t.Errorf("posts tile should show 6 published, 1 draft and link to /admin/posts: %s", m)
	}
	if m := tile("pages"); !strings.Contains(m, `href="/admin/pages"`) || !strings.Contains(m, ">1<") {
		t.Errorf("pages tile: %s", m)
	}
	if m := tile("comments"); !strings.Contains(m, `href="/admin/comments"`) || !strings.Contains(m, ">1<") {
		t.Errorf("comments tile: %s", m)
	}
	if m := tile("users"); !strings.Contains(m, `href="/admin/users"`) || !strings.Contains(m, ">2<") {
		t.Errorf("users tile: %s", m)
	}

	// Recent posts: the latest five, newest first, linking to the admin editor.
	rows := regexp.MustCompile(`(?s)<tr class="recent-post">.*?</tr>`).FindAllString(body, -1)
	if len(rows) != 5 {
		t.Fatalf("expected 5 recent posts, got %d", len(rows))
	}
	if !strings.Contains(rows[0], "Dash Post 7") || !strings.Contains(rows[4], "Dash Post 3") {
		t.Errorf("recent posts must be newest first and capped at five: %v", rows)
	}
	var seven blog.Post
	db.Preload("PostType").Where("slug = ?", "dash-post-7").First(&seven)
	if !strings.Contains(body, `href="`+seven.Adminlink()+`"`) {
		t.Errorf("recent post must link to its admin page %s", seven.Adminlink())
	}
	if !strings.Contains(body, "Draft</span>") {
		t.Errorf("the draft post must carry a Draft badge")
	}
	if !strings.Contains(body, "Nice post") || !strings.Contains(body, `id="comment-row-`) {
		t.Errorf("expected the recent comments table")
	}

	// Site panel.
	if !strings.Contains(body, `href="/admin/themes"`) || !strings.Contains(body, ">"+overlayTheme+"<") {
		t.Errorf("site panel must name the active theme and link to /admin/themes")
	}
	if !strings.Contains(body, `href="/admin/plugins"`) || !strings.Contains(body, "No plugins enabled") {
		t.Errorf("site panel must list enabled plugins (none here) and link to /admin/plugins")
	}
	if !strings.Contains(body, "v9.9.9-test") {
		t.Errorf("site panel must show the goblog version")
	}
	if strings.Contains(body, "Welcome to the admin dashboard") {
		t.Errorf("the placeholder welcome text should be gone")
	}
	if !strings.Contains(body, `class="admin-panel"`) {
		t.Errorf("admin content must sit in the opaque admin panel")
	}
}

// TestAdminDashboard_ActiveThemeFromInstaller: when the theme installer is
// wired, its view of the active theme wins over the stored setting.
func TestAdminDashboard_ActiveThemeFromInstaller(t *testing.T) {
	router, a, db, ad := newAdminHarness(t, "default")
	a.On("IsAdmin", mock.Anything).Return(true)
	a.On("IsLoggedIn", mock.Anything).Return(true)
	db.Create(&blog.Setting{Key: "theme", Value: "stale"})
	ad.Themes = &tinstaller.Installer{ActiveTheme: func() string { return "ocean" }}
	body := getHTML(t, router, "/admin/dashboard")
	if !strings.Contains(body, ">ocean<") || strings.Contains(body, ">stale<") {
		t.Errorf("expected the installer's active theme")
	}
}

// TestAdminEmptyStates: every list page says so when it has nothing to list,
// with a call to action where one exists (#596).
func TestAdminEmptyStates(t *testing.T) {
	for _, theme := range []string{"default", overlayTheme} {
		t.Run(theme, func(t *testing.T) {
			router, a, _, _ := newAdminHarness(t, theme)
			a.On("IsAdmin", mock.Anything).Return(true)
			a.On("IsLoggedIn", mock.Anything).Return(true)
			cases := map[string][]string{
				"/admin/posts":      {"No posts yet", `href="/admin/newpost"`},
				"/admin/pages":      {"No pages yet", "createPage()"},
				"/admin/post-types": {"No post types yet"},
				"/admin/comments":   {"No comments yet"},
				"/admin/users":      {"No users yet"},
				"/admin/dashboard":  {"No posts yet", "No comments yet"},
			}
			for path, wants := range cases {
				body := getHTML(t, router, path)
				for _, want := range wants {
					if !strings.Contains(body, want) {
						t.Errorf("%s: missing %q", path, want)
					}
				}
			}
		})
	}
}

// TestAdminPages_RenderInPanel: every admin page wraps its content in the
// opaque panel below the nav, in default and in a theme that ships no admin
// templates of its own (the overlay falls back to default's).
func TestAdminPages_RenderInPanel(t *testing.T) {
	for _, theme := range []string{"default", overlayTheme} {
		t.Run(theme, func(t *testing.T) {
			router, a, db, _ := newAdminHarness(t, theme)
			a.On("IsAdmin", mock.Anything).Return(true)
			a.On("IsLoggedIn", mock.Anything).Return(true)
			pt := blog.PostType{Name: "Post", Slug: "posts"}
			db.Create(&pt)
			post := blog.Post{Title: "Panel Post", Slug: "panel-post", Content: "x", PostTypeID: pt.ID}
			db.Create(&post)
			db.Preload("PostType").First(&post, post.ID)
			page := blog.Page{Title: "P", Slug: "p"}
			db.Create(&page)
			for _, path := range []string{
				"/admin/dashboard", "/admin/posts", "/admin/newpost", "/admin/pages",
				"/admin/pages/" + strconv.Itoa(int(page.ID)), "/admin/comments", "/admin/users",
				"/admin/post-types", "/admin/post-types/" + strconv.Itoa(int(pt.ID)),
				"/admin/plugins", "/admin/themes", post.Adminlink(),
			} {
				body := getHTML(t, router, path)
				if !strings.Contains(body, `class="admin-panel"`) {
					t.Errorf("%s: content is not wrapped in .admin-panel", path)
				}
				if !strings.Contains(body, `href="/css/admin.css"`) {
					t.Errorf("%s: admin.css is not linked", path)
				}
				if !strings.Contains(body, `class="nav-scroller`) {
					t.Errorf("%s: admin nav missing", path)
				}
				if regexp.MustCompile(`class="[^"]*\bh[1-6]\b[^"]*"`).MatchString(body) {
					t.Errorf("%s: Tachyons .h1-.h6 class in use (it sets height, not type size)", path)
				}
				// A page that sets no title gets the site title alone — never
				// a formatting artefact such as "%!s(<nil>)".
				title := regexp.MustCompile(`<title>([^<]*)</title>`).FindStringSubmatch(body)
				if title == nil || strings.Contains(title[1], "nil") || strings.Contains(title[1], "%!") || strings.HasSuffix(strings.TrimSpace(title[1]), ":") {
					t.Errorf("%s: <title> = %q", path, title)
				}
			}
		})
	}
}
