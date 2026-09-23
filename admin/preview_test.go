package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goblog/admin"
	"goblog/auth"
	"goblog/blog"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func previewHarness(t *testing.T, isAdmin bool) *gin.Engine {
	t.Helper()
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&auth.BlogUser{}, &blog.PostType{}, &blog.Post{}, &blog.Setting{}, &blog.Page{})
	a := &Auth{}
	a.On("IsAdmin", mock.Anything).Return(isAdmin)
	a.On("IsLoggedIn", mock.Anything).Return(isAdmin)
	b := blog.New(db, a, "test")
	ad := admin.New(db, a, &b, "test")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("s", cookie.NewStore([]byte("test"))))
	router.POST("/api/v1/preview", ad.Preview)
	return router
}

func postPreview(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// TestPreview_RendersLikeThePublishedPost: the editor's preview must be the
// same rendering the page will show, so it goes through blog.Post.HTML.
func TestPreview_RendersLikeThePublishedPost(t *testing.T) {
	router := previewHarness(t, true)
	md := "# Title\n\nSome **bold** text.\n\n<iframe src=\"https://www.youtube.com/embed/x\"></iframe>\n"
	w := postPreview(t, router, `{"content":`+jsonString(md)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if want := string((blog.Post{Content: md}).HTML()); got.HTML != want {
		t.Errorf("preview = %q, want %q", got.HTML, want)
	}
	if !strings.Contains(got.HTML, "<iframe") {
		t.Errorf("an author's embed must survive the preview: %q", got.HTML)
	}
}

// TestPreview_EmptyAndMalformed: an empty document previews as nothing; a
// body that is not the expected shape is a 400, not a panic.
func TestPreview_EmptyAndMalformed(t *testing.T) {
	router := previewHarness(t, true)
	if w := postPreview(t, router, `{"content":""}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"html":""`) {
		t.Errorf("empty: %d %s", w.Code, w.Body.String())
	}
	if w := postPreview(t, router, `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d %s", w.Code, w.Body.String())
	}
}

// TestPreview_AdminOnly: the endpoint renders whatever it is given with the
// unsanitised author policy, so only an admin may call it.
func TestPreview_AdminOnly(t *testing.T) {
	router := previewHarness(t, false)
	w := postPreview(t, router, `{"content":"# hi"}`)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Not Authorized") {
		t.Errorf("non-admin: %d %s", w.Code, w.Body.String())
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
