package admin

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"goblog/plugins/directory"

	"github.com/gin-gonic/gin"
)

// requireDirectory checks admin auth and that this site hosts a directory.
// It writes the response and returns nil when the caller should stop.
func (a *Admin) requireDirectory(c *gin.Context) *directory.Service {
	if !a.auth.IsAdmin(c) {
		c.JSON(http.StatusUnauthorized, "Not Authorized")
		return nil
	}
	if a.Directory == nil || a.Directory.Service() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "this site does not host a plugin directory"})
		return nil
	}
	return a.Directory.Service()
}

// directoryStatus maps service errors to HTTP status codes; 0 means "not a
// client error".
func directoryStatus(err error) int {
	var ve *directory.ValidationError
	switch {
	case errors.Is(err, directory.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, directory.ErrBadRepo):
		return http.StatusBadRequest
	case errors.Is(err, directory.ErrAlreadyListed), errors.Is(err, directory.ErrUnderReview), errors.Is(err, directory.ErrNameTaken):
		return http.StatusConflict
	case errors.As(err, &ve):
		return http.StatusUnprocessableEntity
	case errors.Is(err, directory.ErrBusy), errors.Is(err, directory.ErrRateLimited):
		return http.StatusTooManyRequests
	}
	return 0
}

func writeDirectoryError(c *gin.Context, err error) {
	if code := directoryStatus(err); code != 0 {
		c.JSON(code, gin.H{"message": err.Error()})
		return
	}
	log.Printf("Directory API: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "internal error"})
}

func repoID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid id"})
		return 0, false
	}
	return uint(id), true
}

// ListDirectoryRepos returns submitted repositories, optionally filtered by ?status=.
func (a *Admin) ListDirectoryRepos(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	list, err := svc.List(strings.TrimSpace(c.Query("status")))
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

// GetDirectoryRepo returns one repository with its full detail document.
func (a *Admin) GetDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	v, d, err := svc.Get(id)
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"repo": v, "detail": d})
}

// AddDirectoryRepo validates a repository and lists it at once: POST {repo}.
func (a *Admin) AddDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	var req struct {
		Repo string `json:"repo"`
	}
	if err := c.BindJSON(&req); err != nil || strings.TrimSpace(req.Repo) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "repo is required"})
		return
	}
	r, err := svc.Add(c.Request.Context(), req.Repo, a.Directory.Token())
	if err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, r)
}

// ApproveDirectoryRepo lists a pending or rejected repository.
func (a *Admin) ApproveDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Approve(id); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "approved"})
}

// RejectDirectoryRepo keeps a repository off the directory: POST {reason}.
func (a *Admin) RejectDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	c.BindJSON(&req) // an empty body is a rejection without a reason
	if err := svc.Reject(id, strings.TrimSpace(req.Reason)); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "rejected"})
}

// RebuildDirectoryRepo re-validates a repository now.
func (a *Admin) RebuildDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Rebuild(c.Request.Context(), id, a.Directory.Token()); err != nil {
		if directoryStatus(err) == 0 {
			// A build failure is the repository's problem, not ours: report
			// it as unprocessable with the registry's message.
			c.JSON(http.StatusUnprocessableEntity, gin.H{"message": err.Error()})
			return
		}
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "rebuilt"})
}

// DelistDirectoryRepo forgets a repository.
func (a *Admin) DelistDirectoryRepo(c *gin.Context) {
	svc := a.requireDirectory(c)
	if svc == nil {
		return
	}
	id, ok := repoID(c)
	if !ok {
		return
	}
	if err := svc.Delist(id); err != nil {
		writeDirectoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "delisted"})
}
