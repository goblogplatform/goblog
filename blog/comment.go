package blog

import (
	"html/template"
	"time"
)

// Comment represents a user comment on a blog post
type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	PostID    uint      `json:"post_id" gorm:"index;not null"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Content   string    `sql:"type:text;" json:"content"`
	IPAddress string    `json:"-"`
	// UserID is the BlogUser who posted the comment while logged in; nil for
	// comments left anonymously (before login was required, or with the
	// comments_require_login setting off).
	UserID *int `json:"user_id" gorm:"index"`
}

// HTML is the comment rendered to HTML on the server. Commenters are
// anonymous, so the result is sanitised: ordinary formatting survives, raw
// HTML, scripts and javascript: URLs do not.
func (c Comment) HTML() template.HTML { return renderComment(c.Content) }
