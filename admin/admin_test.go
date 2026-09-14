package admin_test

import (
	"bytes"
	"encoding/json"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
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
func (m *Auth) ListUsers(offset, limit int) ([]auth.UserListing, int64) {
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

	//get admin
	router.SetFuncMap(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	})
	router.LoadHTMLGlob("../themes/default/templates/*")
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	a.On("IsLoggedIn", mock.Anything).Return(true).Once()
	req, _ = http.NewRequest("GET", "/admin", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
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
	for _, theme := range []string{"default", "forest", "minimal"} {
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
				tmpl := template.Must(template.New("").Funcs(template.FuncMap{
					"rawHTML": func(s string) template.HTML { return template.HTML(s) },
				}).ParseGlob("../templates/shared/*.html"))
				template.Must(tmpl.ParseGlob("../themes/" + theme + "/templates/*.html"))
				router.SetHTMLTemplate(tmpl)
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
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/" + theme + "/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
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
	for _, theme := range []string{"default", "forest", "minimal"} {
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
