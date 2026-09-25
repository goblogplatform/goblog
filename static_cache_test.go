package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRevalidateStatic: goblog's own scripts, styles and theme assets must
// be revalidated on every request, so a browser picks up a new release's
// admin JS or a theme's new CSS instead of running the cached old one.
// Content that is addressed by name (uploads, images) is left cacheable.
func TestRevalidateStatic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(revalidateStatic())
	router.GET("/*any", func(c *gin.Context) { c.String(http.StatusOK, "x") })

	for path, want := range map[string]string{
		"/js/admin-script.js":     "no-cache",
		"/css/admin.css":          "no-cache",
		"/css/base.css":           "no-cache",
		"/theme/css/goblog.css":   "no-cache",
		"/uploads/photo.png":      "",
		"/img/favicon.ico":        "",
		"/":                       "",
		"/posts/2026/01/01/hello": "",
	} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", path, nil)
		router.ServeHTTP(w, req)
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control = %q, want %q", path, got, want)
		}
	}
}
