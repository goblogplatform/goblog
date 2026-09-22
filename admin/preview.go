package admin

import (
	"net/http"

	"goblog/blog"

	"github.com/gin-gonic/gin"
)

// Preview renders unsaved markdown for the editor's Preview tab, through
// exactly the path the published page uses (blog.Post.HTML), so what an
// author sees is what readers get. Admin only: the renderer keeps raw HTML.
func (a *Admin) Preview(c *gin.Context) {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, "Malformed request")
		return
	}
	c.JSON(http.StatusOK, gin.H{"html": string(blog.Post{Content: body.Content}.HTML())})
}
