package blog_test

import (
	"bytes"
	"encoding/json"
	"fmt"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
	"goblog/plugin"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func (m *Auth) ListUsers(int, int) ([]auth.UserListing, int64, error) { return nil, 0, nil }
func (m *Auth) PromoteAdmin(int) error                                { return nil }
func (m *Auth) DemoteAdmin(int) error                                 { return nil }

func TestBlogWorkflow(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{})
	db.AutoMigrate(&blog.PostType{})
	db.AutoMigrate(&blog.Post{})
	db.AutoMigrate(&blog.Tag{})
	db.AutoMigrate(&blog.Comment{})
	db.AutoMigrate(&blog.Page{})
	db.AutoMigrate(&blog.Setting{})
	// This workflow exercises anonymous commenting, so turn off the login
	// requirement (issue #524); the logged-in path has its own tests.
	db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: "false"})

	// Seed default post type
	defaultType := blog.PostType{Name: "Post", Slug: "posts", Description: "Blog posts"}
	db.Create(&defaultType)
	a := &Auth{}

	b := blog.New(db, a, "test")
	admin := admin.New(db, a, &b, "test")

	router := gin.Default()
	store := cookie.NewStore([]byte("changelater"))
	router.Use(sessions.Sessions("www.jasonernst.com", store))

	//json requests
	router.POST("/api/v1/posts", admin.CreatePost)
	router.GET("/api/v1/posts", b.ListPosts)
	router.GET("/api/v1/posts/:yyyy/:mm/:dd/:slug", b.GetPost)

	//html requests
	router.GET("/posts/:yyyy/:mm/:dd/:slug", b.Post)
	router.POST("/comments", b.SubmitComment)
	router.GET("/tag/*name", b.Tag)
	router.GET("/", b.Home)
	router.NoRoute(b.NoRoute)

	router.GET("/search", b.Search)
	router.GET("/login", b.Login)
	router.GET("/logout", b.Logout)

	//list all posts, should be empty
	jsonValue, _ := json.Marshal("")
	req, _ := http.NewRequest("GET", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	expected := `[]`
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if w.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got %v want %v", w.Body.String(), expected)
	}

	//create valid post
	testTag := blog.Tag{
		Name: "test",
	}
	testPost := blog.Post{
		Title:   "Test title",
		Content: "This is some test content",
		Tags:    []blog.Tag{testTag},
	}
	jsonValue, _ = json.Marshal(testPost)
	req, _ = http.NewRequest("POST", "/api/v1/posts", bytes.NewBuffer(jsonValue))
	req.Header.Add("Content-Type", "application/json")
	a.On("IsAdmin", mock.Anything).Return(true).Once()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusCreated, w.Code)
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

	//bad year
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/api/v1/posts/zfaq/12/12/slug", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//bad Month
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/api/v1/posts/2020/zq/12/slug", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//bad day
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/api/v1/posts/2020/12/qf/slug", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//everything good but non-existant
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/api/v1/posts/2020/12/12/slug", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusBadRequest, w.Code)
	}

	//html tests

	//get tag
	router.SetFuncMap(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	})
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false)
	jsonValue, _ = json.Marshal("")
	req, _ = http.NewRequest("GET", "/tag/test", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}

	//get not found tag
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/tag/blah", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusNotFound, w.Code)
	}

	// Create pages so dynamic page resolution works
	writingPage := blog.Page{Title: "Writing", Slug: "posts", PageType: blog.PageTypeWriting, ShowInNav: true, NavOrder: 1, Enabled: true}
	researchPage := blog.Page{Title: "Research", Slug: "research", PageType: "research", ShowInNav: true, NavOrder: 2, Enabled: true}
	aboutPage := blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, ShowInNav: true, NavOrder: 3, Enabled: true, Content: "About page content"}
	tagsPage := blog.Page{Title: "Tags", Slug: "tags", PageType: blog.PageTypeTags, ShowInNav: false, NavOrder: 4, Enabled: true}
	archivesPage := blog.Page{Title: "Archives", Slug: "archives", PageType: blog.PageTypeArchives, ShowInNav: false, NavOrder: 5, Enabled: true}
	db.Create(&writingPage)
	db.Create(&researchPage)
	db.Create(&aboutPage)
	db.Create(&tagsPage)
	db.Create(&archivesPage)

	// Dynamic page: /tags resolves via NoRoute
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/tags", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d for /tags but instead got %d\n", http.StatusOK, w.Code)
	}

	// Create posts in different years/months so we can verify archive sort order
	oldPost := blog.Post{Title: "Old Post", Content: "old", Slug: "old-post", PostTypeID: defaultType.ID}
	oldPost.CreatedAt = time.Date(2023, 3, 15, 0, 0, 0, 0, time.UTC)
	db.Create(&oldPost)
	newPost := blog.Post{Title: "New Post", Content: "new", Slug: "new-post", PostTypeID: defaultType.ID}
	newPost.CreatedAt = time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	db.Create(&newPost)

	// Dynamic page: /archives resolves via NoRoute
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/archives", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d for /archives but instead got %d\n", http.StatusOK, w.Code)
	}
	// Verify archives are sorted newest first
	body := w.Body.String()
	idx2025 := strings.Index(body, "2025")
	idx2023 := strings.Index(body, "2023")
	if idx2025 < 0 || idx2023 < 0 {
		t.Fatal("Expected archives page to contain both 2025 and 2023")
	}
	if idx2025 > idx2023 {
		t.Fatal("Expected 2025 to appear before 2023 in archives (newest first)")
	}
	// Verify months are zero-padded
	if !strings.Contains(body, "2023/03") {
		t.Fatal("Expected zero-padded month 2023/03 in archives")
	}
	if !strings.Contains(body, "2025/11") {
		t.Fatal("Expected 2025/11 in archives")
	}

	//get home
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}

	//no route
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/dfadfasdf", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusNotFound, w.Code)
	}

	//html post as normal user (Post handler calls IsAdmin twice: once for template data, once for conditional)
	a.On("IsAdmin", mock.Anything).Return(false).Twice()
	req, _ = http.NewRequest("GET", "/posts/"+strconv.Itoa(post.CreatedAt.Year())+"/"+strconv.Itoa(int(post.CreatedAt.Month()))+"/"+strconv.Itoa(post.CreatedAt.Day())+"/"+post.Slug, bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), testPost.Title) {
		t.Errorf("Expected to see a post with title: %s but didn't", testPost.Title)
	}
	assertCommentFormProtected(t, w.Body.String())

	//html post as admin (Post handler calls IsAdmin twice)
	a.On("IsAdmin", mock.Anything).Return(true).Twice()
	req, _ = http.NewRequest("GET", "/posts/"+strconv.Itoa(post.CreatedAt.Year())+"/"+strconv.Itoa(int(post.CreatedAt.Month()))+"/"+strconv.Itoa(post.CreatedAt.Day())+"/"+post.Slug, bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), testPost.Title) {
		t.Errorf("Expected to see a post with title: %s but didn't", testPost.Title)
	}
	assertCommentFormProtected(t, w.Body.String())

	//html post not found
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/posts/2020/12/12/slug", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusNotFound, w.Code)
	}

	// Dynamic page: /about resolves via NoRoute
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/about", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d for /about but instead got %d\n", http.StatusOK, w.Code)
	}

	// Post type listing: /posts resolves via NoRoute as post type listing
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/posts", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d for /posts but instead got %d\n", http.StatusOK, w.Code)
	}

	// Type-prefixed URL: /posts/yyyy/mm/dd/slug resolves via NoRoute
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	a.On("IsLoggedIn", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/posts/"+strconv.Itoa(post.CreatedAt.Year())+"/"+strconv.Itoa(int(post.CreatedAt.Month()))+"/"+strconv.Itoa(post.CreatedAt.Day())+"/"+post.Slug, bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d for type-prefixed post URL but instead got %d\n", http.StatusOK, w.Code)
	}

	//search with matching query
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/search?q=Test", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), testPost.Title) {
		t.Errorf("Expected search results to contain post title: %s", testPost.Title)
	}

	//search should exclude draft posts
	draftPost := blog.Post{
		Title:   "Draft Secret Post",
		Content: "This draft content should not appear in search",
		Draft:   true,
	}
	db.Create(&draftPost)

	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/search?q=Draft+Secret", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), "0 results found") {
		t.Errorf("Expected '0 results found' for draft-only search query")
	}

	//search with non-matching query
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/search?q=zzzznonexistent", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), "No results found") {
		t.Errorf("Expected 'No results found' message in empty search results")
	}
	if !strings.Contains(w.Body.String(), "0 results found") {
		t.Errorf("Expected '0 results found' in empty search results")
	}

	//logout
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/logout", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()

	router.ServeHTTP(w, req)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusTemporaryRedirect, w.Code)
	}

	//login (note: doesn't test actual login, just showing the login form)
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/login", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusOK, w.Code)
	}
	if !strings.Contains(w.Body.String(), `var next = "/"`) {
		t.Errorf("expected the login page to default its post-login destination to /")
	}

	// login with a return-to path (issue #524); an off-site one is dropped
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/login?next=/posts/2026/09/14/hi", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `var next = "/posts/2026/09/14/hi"`) {
		t.Errorf("expected the login page to carry the next path, body: %.300s", w.Body.String())
	}
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/login?next=//evil.example.com", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `var next = "/"`) {
		t.Errorf("expected an off-site next to fall back to /")
	}

	//login without the .env file
	os.Rename("local.env", "local.env.old")
	a.On("IsAdmin", mock.Anything).Return(false).Once()
	req, _ = http.NewRequest("GET", "/login", bytes.NewBuffer(jsonValue))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected to get status %d but instead got %d\n", http.StatusInternalServerError, w.Code)
	}
	os.Rename("local.env.old", "local.env")

	// Comment tests

	// Token + script-derived check that a real browser would submit (see TestSubmitCommentSpamCheck)
	commentToken := b.CommentTokenAt(post.ID, time.Now().Add(-10*time.Second))
	spamCheck := "&comment_token=" + url.QueryEscape(commentToken) + "&comment_check=" + url.QueryEscape(reverseString(commentToken))

	// Valid comment submission -> 303 redirect
	formData := "post_id=" + strconv.Itoa(int(post.ID)) + "&name=TestUser&content=Great+post!&redirect=" + url.QueryEscape(post.Permalink()) + spamCheck
	req, _ = http.NewRequest("POST", "/comments", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected status %d for valid comment but got %d", http.StatusSeeOther, w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "#comment-") {
		t.Errorf("Expected redirect to contain #comment- anchor but got: %s", location)
	}

	// Missing fields -> error redirect
	formData = "post_id=" + strconv.Itoa(int(post.ID)) + "&name=&content=&redirect=" + url.QueryEscape(post.Permalink())
	req, _ = http.NewRequest("POST", "/comments", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected status %d for missing fields but got %d", http.StatusSeeOther, w.Code)
	}
	location = w.Header().Get("Location")
	if !strings.Contains(location, "comment_error=missing_fields") {
		t.Errorf("Expected redirect with missing_fields error but got: %s", location)
	}

	// Honeypot filled -> silent redirect (no error)
	formData = "post_id=" + strconv.Itoa(int(post.ID)) + "&name=Bot&content=spam&website=http://spam.com&redirect=" + url.QueryEscape(post.Permalink())
	req, _ = http.NewRequest("POST", "/comments", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected status %d for honeypot but got %d", http.StatusSeeOther, w.Code)
	}
	location = w.Header().Get("Location")
	if strings.Contains(location, "comment_error") {
		t.Errorf("Honeypot should silently redirect without error, but got: %s", location)
	}

	// Rate limiting -> error redirect (already posted above from same IP)
	formData = "post_id=" + strconv.Itoa(int(post.ID)) + "&name=TestUser2&content=Another+comment&redirect=" + url.QueryEscape(post.Permalink()) + spamCheck
	req, _ = http.NewRequest("POST", "/comments", strings.NewReader(formData))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected status %d for rate limit but got %d", http.StatusSeeOther, w.Code)
	}
	location = w.Header().Get("Location")
	if !strings.Contains(location, "comment_error=rate_limit") {
		t.Errorf("Expected redirect with rate_limit error but got: %s", location)
	}
}

func TestBacklinks(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Backlink{}, &blog.ExternalBacklink{})
	a := &Auth{}

	b := blog.New(db, a, "test")

	// Create two posts. Post B will link to Post A.
	postA := blog.Post{
		Title:   "Post A",
		Content: "This is Post A content",
		Slug:    "post-a",
	}
	db.Create(&postA)

	// Post B links to Post A using a markdown link
	postB := blog.Post{
		Title:   "Post B",
		Content: "Check out [Post A](/posts/" + postA.CreatedAt.Format("2006/1/2") + "/post-a) for more info",
		Slug:    "post-b",
	}
	db.Create(&postB)

	// Compute backlinks for Post B
	b.ComputeBacklinks(&postB)

	// Post A should have Post B as a backlink
	backlinks := b.GetBacklinks(postA.ID)
	if len(backlinks) != 1 {
		t.Fatalf("Expected 1 backlink for Post A, got %d", len(backlinks))
	}
	if backlinks[0].ID != postB.ID {
		t.Errorf("Expected backlink from Post B (ID %d), got ID %d", postB.ID, backlinks[0].ID)
	}

	// Post B should have Post A as an outbound link
	outbound := b.GetOutboundLinks(postB.ID)
	if len(outbound) != 1 {
		t.Fatalf("Expected 1 outbound link for Post B, got %d", len(outbound))
	}
	if outbound[0].ID != postA.ID {
		t.Errorf("Expected outbound link to Post A (ID %d), got ID %d", postA.ID, outbound[0].ID)
	}

	// Post A should have no outbound links
	outboundA := b.GetOutboundLinks(postA.ID)
	if len(outboundA) != 0 {
		t.Errorf("Expected 0 outbound links for Post A, got %d", len(outboundA))
	}

	// Post B should have no backlinks
	backlinksB := b.GetBacklinks(postB.ID)
	if len(backlinksB) != 0 {
		t.Errorf("Expected 0 backlinks for Post B, got %d", len(backlinksB))
	}

	// Update Post B to remove the link, backlinks should be cleared
	postB.Content = "Updated content with no links"
	db.Save(&postB)
	b.ComputeBacklinks(&postB)

	backlinks = b.GetBacklinks(postA.ID)
	if len(backlinks) != 0 {
		t.Errorf("Expected 0 backlinks after removing link, got %d", len(backlinks))
	}
}

func TestGetNavPages(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{}, &blog.PostType{}, &blog.Post{}, &blog.Setting{})
	a := &Auth{}

	b := blog.New(db, a, "test")

	// Create pages with various states
	db.Create(&blog.Page{Title: "Writing", Slug: "posts", PageType: blog.PageTypeWriting, ShowInNav: true, NavOrder: 2, Enabled: true})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, ShowInNav: true, NavOrder: 1, Enabled: true})
	db.Create(&blog.Page{Title: "Hidden", Slug: "hidden", PageType: blog.PageTypeCustom, ShowInNav: false, NavOrder: 3, Enabled: true})
	db.Create(&blog.Page{Title: "Disabled", Slug: "disabled", PageType: blog.PageTypeCustom, ShowInNav: true, NavOrder: 4, Enabled: false})

	pages := b.GetNavPages()
	if len(pages) != 2 {
		t.Fatalf("Expected 2 nav pages, got %d", len(pages))
	}
	// Should be ordered by nav_order: About (1), Writing (2)
	if pages[0].Slug != "about" {
		t.Errorf("Expected first nav page to be 'about', got '%s'", pages[0].Slug)
	}
	if pages[1].Slug != "posts" {
		t.Errorf("Expected second nav page to be 'posts', got '%s'", pages[1].Slug)
	}
}

func TestGetPageBySlug(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{}, &blog.PostType{}, &blog.Post{}, &blog.Setting{})
	a := &Auth{}

	b := blog.New(db, a, "test")

	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, Enabled: true})
	db.Create(&blog.Page{Title: "Disabled", Slug: "disabled-page", PageType: blog.PageTypeCustom, Enabled: false})

	// Enabled page found
	page, err := b.GetPageBySlug("about")
	if err != nil {
		t.Fatalf("Expected to find page 'about', got error: %v", err)
	}
	if page.Title != "About" {
		t.Errorf("Expected page title 'About', got '%s'", page.Title)
	}

	// Disabled page not found
	_, err = b.GetPageBySlug("disabled-page")
	if err == nil {
		t.Error("Expected error for disabled page, got nil")
	}

	// Non-existent page not found
	_, err = b.GetPageBySlug("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent page, got nil")
	}
}

func TestExternalBacklinks(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Backlink{}, &blog.ExternalBacklink{})
	a := &Auth{}

	b := blog.New(db, a, "test")

	post := blog.Post{
		Title:   "Test Post",
		Content: "Some content",
		Slug:    "test-post",
	}
	db.Create(&post)

	router := gin.Default()
	store := cookie.NewStore([]byte("changelater"))
	router.Use(sessions.Sessions("test", store))

	// Test external referer is tracked
	router.GET("/track", func(c *gin.Context) {
		b.TrackReferer(c, post.ID)
		c.String(http.StatusOK, "ok")
	})

	// Request with external referer
	req, _ := http.NewRequest("GET", "/track", nil)
	req.Header.Set("Referer", "https://example.com/some-page")
	req.Host = "myblog.com"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	backlinks := b.GetExternalBacklinks(post.ID)
	if len(backlinks) != 1 {
		t.Fatalf("Expected 1 external backlink, got %d", len(backlinks))
	}
	if backlinks[0].Referer != "https://example.com/some-page" {
		t.Errorf("Expected referer 'https://example.com/some-page', got '%s'", backlinks[0].Referer)
	}
	if backlinks[0].HitCount != 1 {
		t.Errorf("Expected hit count 1, got %d", backlinks[0].HitCount)
	}

	// Second request from same referer should increment hit count
	req, _ = http.NewRequest("GET", "/track", nil)
	req.Header.Set("Referer", "https://example.com/some-page")
	req.Host = "myblog.com"
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	backlinks = b.GetExternalBacklinks(post.ID)
	if len(backlinks) != 1 {
		t.Fatalf("Expected 1 external backlink after second hit, got %d", len(backlinks))
	}
	if backlinks[0].HitCount != 2 {
		t.Errorf("Expected hit count 2 after second hit, got %d", backlinks[0].HitCount)
	}

	// Self-referral should be skipped
	req, _ = http.NewRequest("GET", "/track", nil)
	req.Header.Set("Referer", "https://myblog.com/other-page")
	req.Host = "myblog.com"
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	backlinks = b.GetExternalBacklinks(post.ID)
	if len(backlinks) != 1 {
		t.Errorf("Expected self-referral to be skipped, got %d backlinks", len(backlinks))
	}

	// Self-referral with port mismatch should still be skipped
	req, _ = http.NewRequest("GET", "/track", nil)
	req.Header.Set("Referer", "https://myblog.com:443/other-page")
	req.Host = "myblog.com:8080"
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	backlinks = b.GetExternalBacklinks(post.ID)
	if len(backlinks) != 1 {
		t.Errorf("Expected self-referral with different port to be skipped, got %d backlinks", len(backlinks))
	}

	// Empty referer should be skipped
	req, _ = http.NewRequest("GET", "/track", nil)
	req.Host = "myblog.com"
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	backlinks = b.GetExternalBacklinks(post.ID)
	if len(backlinks) != 1 {
		t.Errorf("Expected empty referer to be skipped, got %d backlinks", len(backlinks))
	}
}

// TestExternalBacklinksSelfReferralBehindProxy covers issue #539: behind a
// reverse proxy c.Request.Host is the upstream address (e.g. localhost:7000),
// so self-referrals must also be detected via X-Forwarded-Host, the configured
// site_url, and IP-literal / localhost referers.
func TestExternalBacklinksSelfReferralBehindProxy(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Backlink{}, &blog.ExternalBacklink{}, &blog.Setting{})
	a := &Auth{}
	b := blog.New(db, a, "test")

	post := blog.Post{Title: "Test Post", Content: "Some content", Slug: "test-post"}
	db.Create(&post)
	db.Create(&blog.Setting{Key: "site_url", Type: "text", Value: "https://www.myblog.com"})

	router := gin.Default()
	router.GET("/track", func(c *gin.Context) {
		b.TrackReferer(c, post.ID)
		c.String(http.StatusOK, "ok")
	})

	send := func(referer, host, forwardedHost string) {
		req, _ := http.NewRequest("GET", "/track", nil)
		req.Header.Set("Referer", referer)
		req.Host = host
		if forwardedHost != "" {
			req.Header.Set("X-Forwarded-Host", forwardedHost)
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	cases := []struct {
		name, referer, host, forwardedHost string
	}{
		{"site_url host with proxy-rewritten Host", "https://www.myblog.com/other-page", "localhost:7000", ""},
		{"site_url host without www", "https://myblog.com/other-page", "localhost:7000", ""},
		{"X-Forwarded-Host match", "https://blog.example.org/other-page", "localhost:7000", "blog.example.org:443"},
		{"X-Forwarded-Host match ignoring www", "https://www.blog.example.org/other-page", "localhost:7000", "blog.example.org"},
		{"X-Forwarded-Host chained proxy list", "https://blog.example.org/other-page", "localhost:7000", "edge.internal, blog.example.org"},
		{"Request.Host match ignoring www", "https://www.myblog.com/other-page", "myblog.com", ""},
		{"IPv4 literal referer", "https://159.89.157.125/other-page", "localhost:7000", ""},
		{"IPv6 literal referer", "http://[::1]:7000/other-page", "localhost:7000", ""},
		{"localhost referer", "http://localhost:7000/other-page", "localhost:7000", ""},
	}
	for _, tc := range cases {
		send(tc.referer, tc.host, tc.forwardedHost)
		if got := b.GetExternalBacklinks(post.ID); len(got) != 0 {
			t.Errorf("%s: expected self-referral %q to be skipped, got %d backlinks: %+v", tc.name, tc.referer, len(got), got)
		}
	}

	// A genuinely external referer must still be recorded under the same proxy conditions.
	send("https://example.com/some-page", "localhost:7000", "www.myblog.com")
	got := b.GetExternalBacklinks(post.ID)
	if len(got) != 1 || got[0].Referer != "https://example.com/some-page" {
		t.Fatalf("expected external referer to be tracked, got %+v", got)
	}

	// With no site_url configured, tracking must still work (no panic) and fall
	// back to the request headers for self detection.
	db.Where("key = ?", "site_url").Delete(&blog.Setting{})
	send("https://www.myblog.com/other-page", "localhost:7000", "")
	send("https://other.example/page", "localhost:7000", "")
	got = b.GetExternalBacklinks(post.ID)
	if len(got) != 3 {
		t.Fatalf("expected www.myblog.com to be tracked as external once site_url is unset (3 rows total), got %+v", got)
	}
}

// reverseString mirrors what the inline comment-form script does to fill comment_check.
func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// TestSubmitCommentSpamCheck covers issue #542: comments must carry a signed,
// server-issued token and a JS-derived check value, and must not be submitted
// implausibly fast or with a stale token.
func TestSubmitCommentSpamCheck(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Setting{})
	// Anonymous submissions: the anti-spam checks are independent of the
	// login requirement (issue #524), which is tested separately.
	db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: "false"})
	a := &Auth{}
	b := blog.New(db, a, "test")

	post := blog.Post{Title: "Test Post", Content: "Some content", Slug: "test-post"}
	db.Create(&post)
	other := blog.Post{Title: "Other Post", Content: "Other content", Slug: "other-post"}
	db.Create(&other)

	router := gin.Default()
	router.POST("/comments", b.SubmitComment)

	submit := func(name, token, check string) string {
		form := url.Values{}
		form.Set("post_id", strconv.Itoa(int(post.ID)))
		form.Set("name", name)
		form.Set("content", "Hello there")
		form.Set("redirect", "/p")
		form.Set("comment_token", token)
		form.Set("comment_check", check)
		req, _ := http.NewRequest("POST", "/comments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10.0.0." + strconv.Itoa(len(name)) + ":1234" // distinct IP per case to dodge the rate limiter
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s: expected 303, got %d", name, w.Code)
		}
		return w.Header().Get("Location")
	}
	countComments := func() int64 {
		var n int64
		db.Model(&blog.Comment{}).Count(&n)
		return n
	}

	now := time.Now()
	valid := b.CommentTokenAt(post.ID, now.Add(-10*time.Second))

	rejected := []struct {
		name, token, check string
	}{
		{"a", "", ""},
		{"ab", "not-a-token", reverseString("not-a-token")},
		{"abc", valid + "x", reverseString(valid + "x")},
		{"abcd", b.CommentTokenAt(other.ID, now.Add(-10*time.Second)), reverseString(b.CommentTokenAt(other.ID, now.Add(-10*time.Second)))},
		{"abcde", b.CommentTokenAt(post.ID, now), reverseString(b.CommentTokenAt(post.ID, now))},
		{"abcdef", b.CommentTokenAt(post.ID, now.Add(-25*time.Hour)), reverseString(b.CommentTokenAt(post.ID, now.Add(-25*time.Hour)))},
		{"abcdefg", valid, ""},
		{"abcdefgh", valid, valid},
	}
	labels := []string{"missing token", "garbage token", "tampered signature", "token for another post", "submitted too fast", "expired token", "missing check", "check not derived by script"}
	for i, tc := range rejected {
		loc := submit(tc.name, tc.token, tc.check)
		if !strings.Contains(loc, "comment_error=spam_check") {
			t.Errorf("%s: expected spam_check error redirect, got %s", labels[i], loc)
		}
	}
	if n := countComments(); n != 0 {
		t.Fatalf("expected no comments stored after rejected submissions, got %d", n)
	}

	loc := submit("abcdefghi", valid, reverseString(valid))
	if !strings.Contains(loc, "#comment-") {
		t.Errorf("valid token and check: expected success redirect, got %s", loc)
	}
	if n := countComments(); n != 1 {
		t.Fatalf("expected 1 comment stored after valid submission, got %d", n)
	}

	// The token rendered into the page must verify the same way.
	rendered := b.CommentToken(post.ID)
	if rendered == "" || strings.Count(rendered, ".") != 1 {
		t.Errorf("expected CommentToken to return a '<ts>.<sig>' token, got %q", rendered)
	}
}

// assertCommentFormProtected checks a rendered post page carries the anti-spam
// token, the empty check field, and the script that fills it (issue #542).
func assertCommentFormProtected(t *testing.T, body string) {
	t.Helper()
	if !regexp.MustCompile(`name="comment_token" value="\d+\.[0-9a-f]{64}"`).MatchString(body) {
		t.Errorf("expected rendered comment form to contain a signed comment_token, body: %.200s...", body)
	}
	if !strings.Contains(body, `name="comment_check" value=""`) {
		t.Errorf("expected rendered comment form to contain an empty comment_check field")
	}
	if !strings.Contains(body, `getElementById("comment_check")`) {
		t.Errorf("expected rendered comment form to include the script that fills comment_check")
	}
}

// TestGetComments covers the paginated, newest-first comment listing used by
// the admin comments page (issue #545).
func TestGetComments(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Comment{})
	b := blog.New(db, &Auth{}, "test")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		db.Create(&blog.Comment{PostID: 1, Name: "c" + strconv.Itoa(i), Content: "x", CreatedAt: base.Add(time.Duration(i) * time.Hour)})
	}

	comments, total := b.GetComments(0, 2)
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(comments) != 2 || comments[0].Name != "c5" || comments[1].Name != "c4" {
		t.Errorf("expected first page [c5 c4], got %+v", comments)
	}

	comments, _ = b.GetComments(4, 2)
	if len(comments) != 1 || comments[0].Name != "c1" {
		t.Errorf("expected last page [c1], got %+v", comments)
	}

	comments, total = b.GetComments(10, 2)
	if len(comments) != 0 || total != 5 {
		t.Errorf("expected empty page beyond the end with total 5, got %+v total %d", comments, total)
	}
}

// TestDashboardCounts covers the cheap counters and the recent-posts query
// the admin dashboard is built from.
func TestDashboardCounts(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{})
	b := blog.New(db, &Auth{}, "test")

	if pub, drafts := b.CountPosts(); pub != 0 || drafts != 0 {
		t.Errorf("empty blog: posts = %d published, %d drafts", pub, drafts)
	}
	if b.CountPages() != 0 || b.CountComments() != 0 || len(b.GetRecentPosts(5)) != 0 {
		t.Error("empty blog should count nothing")
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 7; i++ {
		db.Create(&blog.Post{Title: "p" + strconv.Itoa(i), Slug: "p" + strconv.Itoa(i), Content: "x", Draft: i%3 == 0, CreatedAt: base.Add(time.Duration(i) * time.Hour)})
	}
	db.Create(&blog.Page{Title: "About", Slug: "about"})
	db.Create(&blog.Page{Title: "Hidden", Slug: "hidden", Enabled: false})
	db.Create(&blog.Comment{PostID: 1, Name: "c", Content: "x"})

	if pub, drafts := b.CountPosts(); pub != 5 || drafts != 2 {
		t.Errorf("posts = %d published, %d drafts; want 5 and 2", pub, drafts)
	}
	if n := b.CountPages(); n != 2 {
		t.Errorf("pages = %d, want 2 (disabled pages count too)", n)
	}
	if n := b.CountComments(); n != 1 {
		t.Errorf("comments = %d, want 1", n)
	}
	recent := b.GetRecentPosts(5)
	if len(recent) != 5 || recent[0].Title != "p7" || recent[4].Title != "p3" {
		t.Errorf("recent = %+v; want p7..p3 newest first", recent)
	}
	if !recent[1].Draft {
		t.Error("recent posts must include drafts")
	}
}

// newCommentFixture builds a blog with one post and a /comments route, returning
// a submit helper. The mock auth's user field controls who is logged in and
// the settings table starts empty, so comments_require_login is at its default.
func newCommentFixture(t *testing.T) (*gorm.DB, blog.Blog, *Auth, *blog.Post, func(fields map[string]string) (string, int)) {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Setting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	post := blog.Post{Title: "Test Post", Content: "Some content", Slug: "test-post"}
	db.Create(&post)

	router := gin.Default()
	router.POST("/comments", b.SubmitComment)
	ip := 0
	submit := func(fields map[string]string) (string, int) {
		t.Helper()
		token := b.CommentTokenAt(post.ID, time.Now().Add(-10*time.Second))
		form := url.Values{}
		form.Set("post_id", strconv.Itoa(int(post.ID)))
		form.Set("content", "Hello there")
		form.Set("redirect", "/p")
		form.Set("comment_token", token)
		form.Set("comment_check", reverseString(token))
		for k, v := range fields {
			form.Set(k, v)
		}
		req, _ := http.NewRequest("POST", "/comments", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		ip++
		req.RemoteAddr = "10.1.0." + strconv.Itoa(ip) + ":1234" // fresh IP per call to dodge the rate limiter
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Header().Get("Location"), w.Code
	}
	return db, b, a, &post, submit
}

func TestSubmitComment_RequiresLoginByDefault(t *testing.T) {
	db, _, _, _, submit := newCommentFixture(t)
	loc, code := submit(map[string]string{"name": "anon"})
	if code != http.StatusSeeOther || !strings.Contains(loc, "comment_error=login_required") {
		t.Fatalf("expected login_required redirect, got %d %s", code, loc)
	}
	var n int64
	db.Model(&blog.Comment{}).Count(&n)
	if n != 0 {
		t.Fatalf("expected no comment stored, got %d", n)
	}
}

func TestSubmitComment_AnonymousAllowedWhenSettingOff(t *testing.T) {
	db, _, _, _, submit := newCommentFixture(t)
	db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: "false"})
	loc, _ := submit(map[string]string{"name": "anon", "email": "anon@example.com"})
	if !strings.Contains(loc, "#comment-") {
		t.Fatalf("expected success redirect, got %s", loc)
	}
	var c blog.Comment
	db.First(&c)
	if c.Name != "anon" || c.Email != "anon@example.com" || c.UserID != nil {
		t.Fatalf("expected anonymous comment with form values and no user, got %+v", c)
	}
}

func TestSubmitComment_LoggedInUsesAccountEmailAndRecordsUser(t *testing.T) {
	db, _, a, _, submit := newCommentFixture(t)
	user := auth.BlogUser{Provider: auth.ProviderEmail, ProviderID: "real@example.com", Login: "real@example.com", Email: "real@example.com", AccessToken: "tok"}
	db.Create(&user)
	a.user = &user

	loc, _ := submit(map[string]string{"name": "Real Person", "email": "spoofed@example.com"})
	if !strings.Contains(loc, "#comment-") {
		t.Fatalf("expected success redirect, got %s", loc)
	}
	var c blog.Comment
	db.First(&c)
	if c.Name != "Real Person" {
		t.Errorf("expected the submitted name, got %q", c.Name)
	}
	if c.Email != "real@example.com" {
		t.Errorf("expected the account email, got %q", c.Email)
	}
	if c.UserID == nil || *c.UserID != user.ID {
		t.Errorf("expected UserID %d, got %v", user.ID, c.UserID)
	}
}

func TestSubmitComment_LoggedInBlankNameFallsBackToDisplayName(t *testing.T) {
	db, _, a, _, submit := newCommentFixture(t)
	user := auth.BlogUser{Provider: auth.ProviderGitHub, ProviderID: "1", Login: "compscidr", Email: "j@example.com", AccessToken: "tok"}
	db.Create(&user)
	a.user = &user

	loc, _ := submit(map[string]string{"name": "  "})
	if !strings.Contains(loc, "#comment-") {
		t.Fatalf("expected success redirect, got %s", loc)
	}
	var c blog.Comment
	db.First(&c)
	if c.Name != "compscidr" {
		t.Errorf("expected display name fallback, got %q", c.Name)
	}
}

func TestSubmitComment_LoggedInStillNeedsContent(t *testing.T) {
	_, _, a, _, submit := newCommentFixture(t)
	a.user = &auth.BlogUser{ID: 7, Login: "someone"}
	loc, _ := submit(map[string]string{"content": ""})
	if !strings.Contains(loc, "comment_error=missing_fields") {
		t.Fatalf("expected missing_fields redirect, got %s", loc)
	}
}

func TestCommentsRequireLogin_Setting(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Setting{})
	b := blog.New(db, &Auth{}, "test")
	if !b.CommentsRequireLogin() {
		t.Fatal("expected login to be required when the setting row is missing")
	}
	db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: "false"})
	if b.CommentsRequireLogin() {
		t.Fatal("expected login not to be required when the setting is false")
	}
	db.Model(&blog.Setting{}).Where("key = ?", "comments_require_login").Update("value", "true")
	if !b.CommentsRequireLogin() {
		t.Fatal("expected login to be required when the setting is true")
	}
}

// renderPostPage renders the post page with the given theme, logged-in user
// (nil for anonymous) and comments_require_login value, through the /posts/
// route. See renderPostPageVia for the NoRoute-resolved URL forms.
func renderPostPage(t *testing.T, theme string, user *auth.BlogUser, requireLogin string) string {
	t.Helper()
	return renderPostPageVia(t, theme, user, false, requireLogin, "/posts/%s")
}

// renderPostPageVia renders a post whose permalink date is substituted into
// pathFmt ("/posts/%s" for the routed handler, "/%s" for the legacy
// date-only URL that NoRoute resolves) as an admin or a regular visitor.
func renderPostPageVia(t *testing.T, theme string, user *auth.BlogUser, admin bool, requireLogin string, pathFmt string) string {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Setting{}, &blog.Page{}, &blog.Backlink{}, &blog.ExternalBacklink{})
	db.Create(&blog.Setting{Key: "comments_require_login", Type: "checkbox", Value: requireLogin})
	a := &Auth{user: user}
	a.On("IsAdmin", mock.Anything).Return(admin)
	a.On("IsLoggedIn", mock.Anything).Return(user != nil)
	b := blog.New(db, a, "test")
	post := blog.Post{Title: "Render Post", Content: "Body", Slug: "render-post"}
	db.Create(&post)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/" + theme + "/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/posts/:yyyy/:mm/:dd/:slug", b.Post)
	router.NoRoute(b.NoRoute)
	datePath := strings.TrimPrefix(post.Permalink(), "/posts/")
	req, _ := http.NewRequest("GET", fmt.Sprintf(pathFmt, datePath), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d", theme, w.Code)
	}
	return w.Body.String()
}

// TestNoRoutePost_CommentFormFollowsLoginState covers the same states for
// posts resolved by NoRoute (legacy /yyyy/mm/dd/slug URLs), for both the
// visitor and the admin rendering, which use a separate render path.
func TestNoRoutePost_CommentFormFollowsLoginState(t *testing.T) {
	t.Run("visitor, required and logged out", func(t *testing.T) {
		body := renderPostPageVia(t, "default", nil, false, "true", "/%s")
		if strings.Contains(body, `action="/comments"`) || !strings.Contains(body, `href="/login?next=`) {
			t.Error("expected a login link instead of the comment form")
		}
	})
	t.Run("visitor, logged in", func(t *testing.T) {
		user := &auth.BlogUser{ID: 3, Login: "someone", Email: "someone@example.com"}
		body := renderPostPageVia(t, "default", user, false, "true", "/%s")
		assertCommentFormProtected(t, body)
		if !strings.Contains(body, "Commenting as someone@example.com") {
			t.Error("expected the logged-in commenter note")
		}
	})
	t.Run("admin", func(t *testing.T) {
		user := &auth.BlogUser{ID: 1, Login: "admin", Email: "admin@example.com"}
		body := renderPostPageVia(t, "default", user, true, "true", "/%s")
		if !strings.Contains(body, "Render Post") {
			t.Error("expected the admin post view to render")
		}
	})
}

// TestPostPage_CommentFormFollowsLoginState covers the comment section's three
// states (issue #524) in the built-in theme (directory themes ship their own
// post.html and are checked in their own repositories).
func TestPostPage_CommentFormFollowsLoginState(t *testing.T) {
	for _, theme := range []string{"default"} {
		t.Run(theme+"/required and logged out", func(t *testing.T) {
			body := renderPostPage(t, theme, nil, "true")
			if strings.Contains(body, `action="/comments"`) {
				t.Error("expected no comment form")
			}
			if !strings.Contains(body, `href="/login?next=`) {
				t.Error("expected a login link that returns to the post")
			}
		})
		t.Run(theme+"/not required and logged out", func(t *testing.T) {
			body := renderPostPage(t, theme, nil, "false")
			assertCommentFormProtected(t, body)
			if !strings.Contains(body, `name="email"`) {
				t.Error("expected the email field for anonymous commenters")
			}
		})
		t.Run(theme+"/logged in", func(t *testing.T) {
			user := &auth.BlogUser{ID: 3, Provider: auth.ProviderEmail, Login: "jason@example.com", Email: "jason@example.com"}
			body := renderPostPage(t, theme, user, "true")
			assertCommentFormProtected(t, body)
			if strings.Contains(body, `name="email"`) {
				t.Error("expected no email field for logged-in commenters")
			}
			if !strings.Contains(body, `name="name" value="jason"`) {
				t.Error("expected the name field prefilled with the display name")
			}
			if !strings.Contains(body, "jason@example.com") {
				t.Error("expected the page to say which account is commenting")
			}
		})
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                              "/",
		"/":                             "/",
		"/posts/2026/09/14/hello":       "/posts/2026/09/14/hello",
		"/posts/x?a=1#comments":         "/posts/x?a=1#comments",
		"//evil.example.com":            "/",
		"/\\evil.example.com":           "/",
		"https://evil.example.com/post": "/",
		"posts/relative":                "/",
		"javascript:alert(1)":           "/",
	}
	for in, want := range cases {
		if got := blog.SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubmitComment_RedirectIsSameSiteOnly(t *testing.T) {
	_, _, _, _, submit := newCommentFixture(t)
	for _, bad := range []string{"https://evil.example.com/", "//evil.example.com", "/\\evil.example.com"} {
		loc, _ := submit(map[string]string{"redirect": bad})
		if !strings.HasPrefix(loc, "/?comment_error=") {
			t.Errorf("redirect %q: expected a same-site fallback, got %s", bad, loc)
		}
	}
}

// subPathPlugin owns the "dir" page and answers "" (listing) and "index.json"
// (raw JSON); everything else is declined.
type subPathPlugin struct {
	plugin.BasePlugin
}

func (p *subPathPlugin) Name() string        { return "dir" }
func (p *subPathPlugin) DisplayName() string { return "Dir" }
func (p *subPathPlugin) Version() string     { return "1.0.0" }
func (p *subPathPlugin) Settings() []plugin.SettingDefinition {
	return []plugin.SettingDefinition{{Key: "enabled", Type: "text", DefaultValue: "true", Label: "Enabled"}}
}
func (p *subPathPlugin) Pages() []plugin.PageDefinition {
	return []plugin.PageDefinition{{PageType: "dir", Title: "Dir", Slug: "dir"}}
}
func (p *subPathPlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	switch ctx.SubPath {
	case "":
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": "<p>LISTING</p>"}
	case "index.json":
		ctx.GinContext.Data(http.StatusOK, "application/json", []byte(`[{"name":"x"}]`))
		return "", nil
	}
	return "", nil
}

// TestPluginPageSubPaths covers issue #552: a plugin-owned page also owns
// everything under its slug, while built-in pages keep single-segment slugs.
func TestPluginPageSubPaths(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", Enabled: true})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, Enabled: true, Content: "about"})

	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")

	reg := plugin.NewRegistry(db)
	reg.Register(&subPathPlugin{})
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.NoRoute(b.NoRoute)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		return w
	}

	if w := get("/dir"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "LISTING") {
		t.Errorf("/dir: code=%d body has LISTING=%v", w.Code, strings.Contains(w.Body.String(), "LISTING"))
	}
	if w := get("/dir/index.json"); w.Code != http.StatusOK || w.Body.String() != `[{"name":"x"}]` || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/dir/index.json: code=%d body=%q type=%q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := get("/dir/nope"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "404") {
		t.Errorf("/dir/nope: expected a 404 page, got code=%d", w.Code)
	}
	// Built-in pages do not get sub-paths.
	if w := get("/about"); w.Code != http.StatusOK {
		t.Errorf("/about: code=%d", w.Code)
	}
	if w := get("/about/anything"); w.Code != http.StatusNotFound {
		t.Errorf("/about/anything: expected 404, got %d", w.Code)
	}
	// A disabled plugin's page and sub-paths show the "not available" page.
	reg.UpdateSetting("dir", "enabled", "false")
	if w := get("/dir/index.json"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "currently disabled") {
		t.Errorf("/dir/index.json disabled: code=%d", w.Code)
	}
}

func TestSettingValue(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Setting{})
	a := &Auth{}
	b := blog.New(db, a, "test")
	if got := b.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("missing setting should return the default, got %q", got)
	}
	db.Create(&blog.Setting{Key: "plugin_directory_url", Type: "text", Value: "https://x.test/i.json"})
	if got := b.SettingValue("plugin_directory_url", "default"); got != "https://x.test/i.json" {
		t.Errorf("got %q", got)
	}
	db.Model(&blog.Setting{}).Where("key = ?", "plugin_directory_url").Update("value", "")
	if got := b.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("empty setting should return the default, got %q", got)
	}
	nodb := blog.New(nil, a, "test")
	if got := nodb.SettingValue("plugin_directory_url", "default"); got != "default" {
		t.Errorf("no db should return the default, got %q", got)
	}
}

// TestUnownedPluginPageIsHidden: a page whose plugin type has no registered
// owner (e.g. "research" after the scholar plugin moved to the directory) is
// hidden from the nav and answers 404 until a plugin claims it again.
func TestUnownedPluginPageIsHidden(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Page{Title: "Research", Slug: "research", PageType: "research", ShowInNav: true, Enabled: true})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, ShowInNav: true, Enabled: true, Content: "about"})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")
	reg := plugin.NewRegistry(db) // no plugin owns "research"
	b.PageFilter = blog.PluginPageFilter(reg)

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.NoRoute(b.NoRoute)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		return w
	}
	if w := get("/research"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Page Not Available") || !strings.Contains(w.Body.String(), "plugin is not installed") {
		t.Errorf("/research without an owner: code=%d, body=%q", w.Code, w.Body.String())
	}
	if w := get("/about"); w.Code != http.StatusOK {
		t.Errorf("/about: code=%d", w.Code)
	}
	for _, p := range b.GetNavPages() {
		if p.PageType == "research" {
			t.Error("unowned research page must not be in the nav")
		}
	}
	// A plugin claiming the type brings the page back.
	reg.Register(&subPathPlugin{}) // owns "dir"
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", ShowInNav: true, Enabled: true})
	reg.Init()
	found := false
	for _, p := range b.GetNavPages() {
		found = found || p.PageType == "dir"
	}
	if !found {
		t.Error("an owned plugin page must be in the nav")
	}
}

// searchingPlugin contributes one site-search result for "hello".
type searchingPlugin struct{ subPathPlugin }

func (p *searchingPlugin) Search(_ *plugin.HookContext, q string) []plugin.SearchResult {
	if q != "hello" {
		return nil
	}
	return []plugin.SearchResult{{Title: "Hello plugin", URL: "/dir/hello", Summary: "matched <b>" + q + "</b>", Kind: "Plugin"}}
}

// TestSearchIncludesPluginResults: /search renders one results list —
// matching posts first, then whatever enabled plugins that implement
// plugin.Searcher contribute — through goblog's shared _search_results
// partial, so a theme need not know what kinds of result exist.
func TestSearchIncludesPluginResults(t *testing.T) {
	for _, theme := range []string{"default"} {
		t.Run(theme, func(t *testing.T) {
			db, _ := gorm.Open(sqlite.Open(":memory:"))
			db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
			pt := blog.PostType{Name: "Post", Slug: "posts"}
			db.Create(&pt)
			db.Create(&blog.Post{Title: "Hello post", Slug: "hello-post", Content: "A post that says hello.", PostTypeID: pt.ID, Tags: []blog.Tag{{Name: "greetings"}}})
			a := &Auth{}
			a.On("IsAdmin", mock.Anything).Return(false)
			a.On("IsLoggedIn", mock.Anything).Return(false)
			b := blog.New(db, a, "test")
			reg := plugin.NewRegistry(db)
			reg.Register(&searchingPlugin{})
			reg.Init()

			router := gin.New()
			router.Use(plugin.Middleware(reg))
			tmpl := template.Must(template.New("").Funcs(template.FuncMap{
				"rawHTML": func(s string) template.HTML { return template.HTML(s) },
			}).ParseGlob("../templates/shared/*.html"))
			template.Must(tmpl.ParseGlob("../themes/" + theme + "/templates/*.html"))
			router.SetHTMLTemplate(tmpl)
			router.GET("/search", b.Search)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/search?q=hello", nil)
			router.ServeHTTP(w, req)
			body := w.Body.String()
			for _, want := range []string{"Hello post", "/hello-post", "#greetings", `href="/dir/hello"`, "Hello plugin", "matched &lt;b&gt;hello&lt;/b&gt;", "2 results found"} {
				if !strings.Contains(body, want) {
					t.Errorf("search page missing %q in:\n%s", want, body)
				}
			}
			if strings.Contains(body, "<b>hello</b>") {
				t.Error("plugin summaries are plain text and must be escaped")
			}
			if strings.Index(body, "Hello post") > strings.Index(body, "Hello plugin") {
				t.Error("posts come before plugin results")
			}
			if !strings.Contains(body, ">Plugin<") || strings.Contains(body, ">Post<") {
				t.Errorf("the kind label is shown for plugin results only:\n%s", body)
			}
			if strings.Contains(body, "No results found") {
				t.Error("the no-results message must not show")
			}

			w = httptest.NewRecorder()
			req, _ = http.NewRequest("GET", "/search?q=zzz", nil)
			router.ServeHTTP(w, req)
			if body := w.Body.String(); !strings.Contains(body, "No results found") || !strings.Contains(body, "0 results found") {
				t.Errorf("no-results page:\n%s", body)
			}

			// Without a query nothing is searched.
			w = httptest.NewRecorder()
			req, _ = http.NewRequest("GET", "/search", nil)
			router.ServeHTTP(w, req)
			if strings.Contains(w.Body.String(), "Hello plugin") {
				t.Error("an empty query must not list plugin results")
			}
		})
	}
}

// TestSearchResultsPartial: a theme's search.html only has to include
// goblog's shared _search_results partial; what a result is (posts, plugin
// hits, their count and the no-results message) is goblog's business.
func TestSearchResultsPartial(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	db.Create(&blog.Post{Title: "Hello post", Slug: "hello-post", Content: "A post that says hello.", PostTypeID: pt.ID, Tags: []blog.Tag{{Name: "greetings"}}})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")
	reg := plugin.NewRegistry(db)
	reg.Register(&searchingPlugin{})
	reg.Init()

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	// A theme that overrides search.html with the bare minimum.
	template.Must(tmpl.New("search.html").Parse(`{{ template "header.html" . }}<main id="bare">{{ template "_search_results" . }}</main>{{ template "footer.html" . }}`))
	router.SetHTMLTemplate(tmpl)
	router.GET("/search", b.Search)

	get := func(q string) string {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/search?q="+q, nil)
		router.ServeHTTP(w, req)
		return w.Body.String()
	}
	body := get("hello")
	for _, want := range []string{`id="bare"`, "2 results found", "Hello post", "/hello-post", "#greetings", `href="/dir/hello"`, "Hello plugin", ">Plugin<", "matched &lt;b&gt;hello&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, ">Post<") {
		t.Error("posts carry no kind label")
	}
	if body := get("zzz"); !strings.Contains(body, "No results found for 'zzz'") || !strings.Contains(body, "0 results found") {
		t.Errorf("no-results:\n%s", body)
	}
	if body := get(""); strings.Contains(body, "results found") || strings.Contains(body, "No results") {
		t.Errorf("no query, no count line:\n%s", body)
	}
}

// sitemapPlugin owns "dir" and lists one URL under it.
type sitemapPlugin struct{ subPathPlugin }

func (p *sitemapPlugin) Sitemap(_ *plugin.HookContext) []plugin.SitemapURL {
	return []plugin.SitemapURL{{Loc: "/dir/hello", LastMod: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
}

// TestSitemap: every URL is under the site_url setting (not a host baked
// into goblog); posts carry lastmod; every enabled page is listed, nav or
// not; pages that do not exist are not invented; enabled plugins that
// implement plugin.Sitemapper add their URLs.
func TestSitemap(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Setting{Key: "site_url", Value: "https://www.example.test/"})
	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	updated := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	db.Create(&blog.Post{Title: "Hello", Slug: "hello", Content: "hi", PostTypeID: pt.ID, CreatedAt: updated, UpdatedAt: updated, Tags: []blog.Tag{{Name: "go"}}})
	db.Create(&blog.Post{Title: "Draft", Slug: "draft", Content: "hi", PostTypeID: pt.ID, Draft: true})
	db.Create(&blog.Page{Title: "About", Slug: "about", PageType: blog.PageTypeAbout, ShowInNav: true, Enabled: true})
	db.Create(&blog.Page{Title: "Hidden", Slug: "hidden", PageType: blog.PageTypeAbout, ShowInNav: false, Enabled: true})
	db.Create(&blog.Page{Title: "Off", Slug: "off", PageType: blog.PageTypeAbout, ShowInNav: true, Enabled: false})
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", ShowInNav: true, Enabled: true})
	a := &Auth{}
	b := blog.New(db, a, "test")
	reg := plugin.NewRegistry(db)
	reg.Register(&sitemapPlugin{})
	reg.Init()
	b.PageFilter = blog.PluginPageFilter(reg)

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	router.GET("/sitemap.xml", b.Sitemap)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/sitemap.xml", nil)
	router.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("code=%d type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"<loc>https://www.example.test/</loc>",
		"<loc>https://www.example.test/about</loc>",
		"<loc>https://www.example.test/hidden</loc>",
		"<loc>https://www.example.test/posts</loc>",
		"<loc>https://www.example.test/posts/2026/08/15/hello</loc>",
		"<lastmod>2026-08-15",
		"<loc>https://www.example.test/tag/go</loc>",
		"<loc>https://www.example.test/dir</loc>",
		"<loc>https://www.example.test/dir/hello</loc>",
		"<lastmod>2026-09-01",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap missing %q in:\n%s", want, body)
		}
	}
	for _, gone := range []string{"jasonernst.com", "/off<", "/draft<", "/archives<", "/tags<"} {
		if strings.Contains(body, gone) {
			t.Errorf("sitemap must not contain %q:\n%s", gone, body)
		}
	}

	// Without site_url the request's own host is used.
	db.Where("key = ?", "site_url").Delete(&blog.Setting{})
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/sitemap.xml", nil)
	req.Host = "blog.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	router.ServeHTTP(w, req)
	if body := w.Body.String(); !strings.Contains(body, "<loc>https://blog.example.test/about</loc>") {
		t.Errorf("no site_url: %s", body)
	}
}

// TestSiteURL_ForwardedProto: proxies chain X-Forwarded-Proto values and
// vary the case; the first one, lower-cased, decides the scheme.
func TestSiteURL_ForwardedProto(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Setting{})
	b := blog.New(db, &Auth{}, "test")
	gin.SetMode(gin.TestMode)
	for header, want := range map[string]string{"https": "https", "HTTPS": "https", "https, http": "https", "http,https": "http", "": "http"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.Host = "h.test"
		if header != "" {
			c.Request.Header.Set("X-Forwarded-Proto", header)
		}
		if got := b.SiteURL(c); got != want+"://h.test" {
			t.Errorf("X-Forwarded-Proto %q: %q", header, got)
		}
	}
}

// TestRobotsTxt: crawlers are told what not to index and where the sitemap is.
func TestRobotsTxt(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Setting{})
	db.Create(&blog.Setting{Key: "site_url", Value: "https://www.example.test"})
	b := blog.New(db, &Auth{}, "test")
	router := gin.New()
	router.GET("/robots.txt", b.RobotsTxt)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/robots.txt", nil)
	router.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("code=%d type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{"User-agent: *", "Disallow: /admin", "Disallow: /search", "Disallow: /api/", "Disallow: /login", "Sitemap: https://www.example.test/sitemap.xml"} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q in:\n%s", want, body)
		}
	}
}

// describingPlugin owns "dir" and describes its page for the <head>.
type describingPlugin struct{ subPathPlugin }

func (p *describingPlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	if ctx.SubPath != "hello" {
		return "", nil
	}
	return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": "<p>hi</p>", "title": "Hello plugin", "meta_description": "Hello says hi to <everyone>."}
}

// TestHeadMetadata: the shared _head gives every page a description,
// canonical link and Open Graph tags from the site settings and the page's
// own data, and structured data that does not name any particular site.
func TestHeadMetadata(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Setting{Key: "site_title", Value: "GoBlog"})
	db.Create(&blog.Setting{Key: "site_subtitle", Value: "Simple blogging"})
	db.Create(&blog.Setting{Key: "site_url", Value: "https://www.example.test"})
	db.Create(&blog.Setting{Key: "site_description", Value: "A blogging platform with a plugin directory."})
	db.Create(&blog.Setting{Key: "favicon", Value: "/img/favicon.ico"})
	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	when := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	db.Create(&blog.Post{Title: "Hello & welcome", Slug: "hello", Content: "First paragraph of the post.", PostTypeID: pt.ID, CreatedAt: when, UpdatedAt: when})
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", ShowInNav: true, Enabled: true})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	a.On("IsWizardMode", mock.Anything).Return(false)
	b := blog.New(db, a, "test")
	reg := plugin.NewRegistry(db)
	reg.Register(&describingPlugin{})
	reg.Init()
	b.PageFilter = blog.PluginPageFilter(reg)

	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/", b.Home)
	router.GET("/posts/:yyyy/:mm/:dd/:slug", b.Post)
	router.GET("/:yyyy/:mm/:dd/:slug", b.Post)
	router.GET("/search", b.Search)
	router.NoRoute(b.NoRoute)
	get := func(path string) string {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		return w.Body.String()
	}
	head := func(body string) string { return body[:strings.Index(body, "</head>")] }
	check := func(t *testing.T, page string, wants ...string) {
		t.Helper()
		h := head(get(page))
		for _, want := range wants {
			if !strings.Contains(h, want) {
				t.Errorf("%s head missing %q in:\n%s", page, want, h)
			}
		}
	}

	check(t, "/",
		`<meta name="description" content="A blogging platform with a plugin directory.">`,
		`<link rel="canonical" href="https://www.example.test/">`,
		`<meta property="og:title" content="GoBlog: Simple blogging">`,
		`<meta property="og:description" content="A blogging platform with a plugin directory.">`,
		`<meta property="og:url" content="https://www.example.test/">`,
		`<meta property="og:type" content="website">`,
		`"@type": "WebSite"`, `"SearchAction"`, `"target": "https:\/\/www.example.test/search?q={search_term_string}"`)
	if h := head(get("/")); strings.Contains(h, "og:image") || strings.Contains(h, "jason.jpg") {
		t.Errorf("no site_image: no og:image and nothing site-specific:\n%s", h)
	}

	// The post is canonical at its permalink whichever URL it was read at.
	for _, u := range []string{"/posts/2026/08/15/hello", "/2026/08/15/hello"} {
		check(t, u,
			`<meta name="description" content="First paragraph of the post.">`,
			`<link rel="canonical" href="https://www.example.test/posts/2026/08/15/hello">`,
			`<meta property="og:type" content="article">`,
			`<meta property="og:url" content="https://www.example.test/posts/2026/08/15/hello">`,
			`<meta property="og:title" content="GoBlog: Hello &amp; welcome">`,
			`"@type": "BlogPosting"`)
	}
	if h := head(get("/posts/2026/08/15/hello")); strings.Contains(h, "jason.jpg") {
		t.Errorf("structured data must not name a site-specific image:\n%s", h)
	}
	// Dates are ISO 8601 and the structured data is valid JSON, images or not.
	db.Create(&blog.Post{Title: "Pictures", Slug: "pics", Content: "![a](/img/a.png) and ![b](/img/b.png)", PostTypeID: pt.ID, CreatedAt: when, UpdatedAt: when})
	for _, u := range []string{"/posts/2026/08/15/hello", "/posts/2026/08/15/pics"} {
		h := head(get(u))
		if !strings.Contains(h, `<meta property="article:published_time" content="2026-08-15T12:00:00Z">`) {
			t.Errorf("%s: dates must be ISO 8601:\n%s", u, h)
		}
		ld := h[strings.Index(h, `<script type="application/ld+json">`)+len(`<script type="application/ld+json">`):]
		ld = ld[:strings.Index(ld, "</script>")]
		var parsed map[string]any
		if err := json.Unmarshal([]byte(ld), &parsed); err != nil {
			t.Errorf("%s: structured data is not JSON (%v):\n%s", u, err, ld)
		} else if u == "/posts/2026/08/15/pics" {
			if imgs, _ := parsed["image"].([]any); len(imgs) != 2 || imgs[0] != "https://www.example.test/img/a.png" {
				t.Errorf("images = %v", parsed["image"])
			}
		} else if parsed["datePublished"] != "2026-08-15T12:00:00Z" {
			t.Errorf("datePublished = %v", parsed["datePublished"])
		}
	}

	// A plugin page describes itself.
	check(t, "/dir/hello",
		`<meta name="description" content="Hello says hi to &lt;everyone&gt;.">`,
		`<link rel="canonical" href="https://www.example.test/dir/hello">`,
		`<meta property="og:title" content="GoBlog: Hello plugin">`)

	// Search results are not for indexing.
	check(t, "/search?q=x", `<meta name="robots" content="noindex, follow"`, `<link rel="canonical" href="https://www.example.test/search">`)

	// With a site image, it is the Open Graph image and the fallback
	// structured-data image.
	db.Create(&blog.Setting{Key: "site_image", Value: "/img/card.png"})
	check(t, "/", `<meta property="og:image" content="https://www.example.test/img/card.png">`)
	check(t, "/posts/2026/08/15/hello", `"https://www.example.test/img/card.png"`)
}

// titlingPlugin owns "dir" and names its sub-page.
type titlingPlugin struct{ subPathPlugin }

func (p *titlingPlugin) RenderPage(ctx *plugin.HookContext, pageType string) (string, gin.H) {
	switch ctx.SubPath {
	case "":
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": "<p>list</p>"}
	case "hello":
		return "page_content.html", gin.H{"has_plugin_content": true, "plugin_content": "<p>hi</p>", "title": "Hello plugin", "page_title": "Hello plugin"}
	}
	return "", nil
}

// TestPluginPageTitle: a plugin page's page_title becomes the page heading
// the theme renders, without the theme knowing; the page row is untouched.
func TestPluginPageTitle(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Page{Title: "Dir", Slug: "dir", PageType: "dir", ShowInNav: true, Enabled: true})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	b := blog.New(db, a, "test")
	reg := plugin.NewRegistry(db)
	reg.Register(&titlingPlugin{})
	reg.Init()
	b.PageFilter = blog.PluginPageFilter(reg)
	router := gin.New()
	router.Use(plugin.Middleware(reg))
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.NoRoute(b.NoRoute)
	get := func(path string) string {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		return w.Body.String()
	}
	if body := get("/dir/hello"); !strings.Contains(body, "<h1>Hello plugin</h1>") || strings.Contains(body, "<h1>Dir</h1>") {
		t.Errorf("sub-page heading:\n%s", body)
	}
	if body := get("/dir"); !strings.Contains(body, "<h1>Dir</h1>") {
		t.Errorf("the page itself keeps its title:\n%s", body)
	}
	var page blog.Page
	db.Where("slug = ?", "dir").First(&page)
	if page.Title != "Dir" {
		t.Error("the page row must not change")
	}
}

// TestRSS: /rss.xml is an RSS 2.0 feed of the newest published posts with
// absolute links and server-rendered HTML bodies; drafts are left out; the
// <head> advertises it.
func TestRSS(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{}, &blog.Page{}, &blog.Setting{}, &plugin.PluginSetting{})
	db.Create(&blog.Setting{Key: "site_title", Value: "GoBlog"})
	db.Create(&blog.Setting{Key: "site_url", Value: "https://www.example.test"})
	db.Create(&blog.Setting{Key: "site_description", Value: "A blog & more"})
	pt := blog.PostType{Name: "Post", Slug: "posts"}
	db.Create(&pt)
	when := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	db.Create(&blog.Post{Title: "Hello & welcome", Slug: "hello", Content: "Some **bold** text.", PostTypeID: pt.ID, CreatedAt: when, UpdatedAt: when})
	db.Create(&blog.Post{Title: "Secret", Slug: "secret", Content: "draft", PostTypeID: pt.ID, Draft: true})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(false)
	a.On("IsLoggedIn", mock.Anything).Return(false)
	a.On("IsWizardMode", mock.Anything).Return(false)
	b := blog.New(db, a, "test")
	router := gin.New()
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"rawHTML": func(s string) template.HTML { return template.HTML(s) },
	}).ParseGlob("../templates/shared/*.html"))
	template.Must(tmpl.ParseGlob("../themes/default/templates/*.html"))
	router.SetHTMLTemplate(tmpl)
	router.GET("/rss.xml", b.RSS)
	router.GET("/", b.Home)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/rss.xml", nil)
	router.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("code=%d type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`<rss version="2.0"`, "<title>GoBlog</title>", "<link>https://www.example.test/</link>", "<description>A blog &amp; more</description>",
		"<title>Hello &amp; welcome</title>", "<link>https://www.example.test/posts/2026/08/15/hello</link>",
		`<guid isPermaLink="true">https://www.example.test/posts/2026/08/15/hello</guid>`,
		"<pubDate>Sat, 15 Aug 2026 12:00:00 +0000</pubDate>", "&lt;strong&gt;bold&lt;/strong&gt;",
		`<atom:link href="https://www.example.test/rss.xml" rel="self" type="application/rss+xml"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feed missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Secret") {
		t.Error("drafts must not be in the feed")
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/", nil)
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `<link rel="alternate" type="application/rss+xml" title="GoBlog" href="https://www.example.test/rss.xml">`) {
		t.Errorf("head must advertise the feed:\n%s", w.Body.String()[:800])
	}
}

// TestPostHTML: a post's markdown rendered on the server, with unsafe HTML
// kept (authors are admins) so embeds keep working.
func TestPostHTML(t *testing.T) {
	p := blog.Post{Content: "# Title\n\nSome **bold** and a [link](/x).\n\n<iframe src=\"https://v.test\"></iframe>"}
	html := string(p.HTML())
	for _, want := range []string{"<h1", "<strong>bold</strong>", `<a href="/x">link</a>`, `<iframe src="https://v.test">`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %q", want, html)
		}
	}
}
