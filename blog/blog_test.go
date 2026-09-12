package blog_test

import (
	"bytes"
	"encoding/json"
	
	"goblog/admin"
	"goblog/auth"
	"goblog/blog"
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
}

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

func TestBlogWorkflow(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{})
	db.AutoMigrate(&blog.PostType{})
	db.AutoMigrate(&blog.Post{})
	db.AutoMigrate(&blog.Tag{})
	db.AutoMigrate(&blog.Comment{})
	db.AutoMigrate(&blog.Page{})

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
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Tag{}, &blog.Comment{})
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
