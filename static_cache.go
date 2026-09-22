package main

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// revalidatePrefixes are the URL prefixes of assets goblog ships and
// replaces in place: its own scripts and styles, and the active theme's
// static files. A browser that caches these heuristically (neither Gin's
// static handler nor c.File sends Cache-Control, so it may) keeps running
// the previous release's admin JS or the previous theme's CSS long after a
// deploy.
var revalidatePrefixes = []string{"/js/", "/css/", "/theme/"}

// revalidateStatic asks browsers to revalidate those assets on every
// request. "no-cache" still allows a cached copy to be reused — the
// browser sends If-Modified-Since and normally gets a 304 — it just may
// not be reused without asking. Content addressed by name (uploads,
// images) is left alone.
func revalidateStatic() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, p := range revalidatePrefixes {
			if strings.HasPrefix(c.Request.URL.Path, p) {
				c.Header("Cache-Control", "no-cache")
				break
			}
		}
		c.Next()
	}
}
