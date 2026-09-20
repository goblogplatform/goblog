package directory

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// Errors the submit page and the admin API turn into messages and status
// codes. Their text is shown to submitters as-is.
var (
	ErrAlreadyListed = errors.New("this repository is already listed in the directory")
	ErrUnderReview   = errors.New("this repository is already under review")
	ErrNameTaken     = errors.New("a different repository already publishes a plugin with this name")
	ErrRateLimited   = errors.New("too many submissions from your address; try again in an hour")
	ErrBusy          = errors.New("another submission is being checked; try again in a minute")
	ErrNotFound      = errors.New("no such repository")
)

// ValidationError is a submission that failed the plugin contract. Msg is
// the registry's error text, the same message CI used to give.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

const (
	submitLimit  = 5 // public submissions per address per submitWindow
	submitWindow = time.Hour
	submitBudget = 90 * time.Second   // one public validation, end to end
	ipRetention  = 7 * 24 * time.Hour // how long submitter_ip is kept
)

// Service is the registry: it validates and builds repositories, keeps the
// curation state in the database and serves the current index from memory.
type Service struct {
	db        *gorm.DB
	newSource func(token string) registry.Source
	validator registry.Validator
	siteURL   func() string
	limiter   *ipLimiter

	// validate serializes builds. Public submissions TryLock it (and get
	// ErrBusy), admin operations wait.
	validate sync.Mutex

	mu           sync.RWMutex
	indexRaw     []byte
	indexEntries []registry.IndexEntry
	lastRefresh  time.Time
}

// NewService wires a Service. newSource is called per operation with the
// current github_token setting so a token change needs no restart.
func NewService(db *gorm.DB, newSource func(token string) registry.Source, val registry.Validator, siteURL func() string) *Service {
	return &Service{db: db, newSource: newSource, validator: val, siteURL: siteURL, limiter: newIPLimiter(submitLimit, submitWindow)}
}

// RepoView is a curation record with its build summarised: what the admin
// list shows.
type RepoView struct {
	ID               uint       `json:"id"`
	Repo             string     `json:"repo"`
	Status           string     `json:"status"`
	SubmittedAt      time.Time  `json:"submitted_at"`
	DecidedAt        *time.Time `json:"decided_at"`
	RejectReason     string     `json:"reject_reason"`
	Name             string     `json:"name"`
	DisplayName      string     `json:"display_name"`
	Version          string     `json:"version"`
	Author           string     `json:"author"`
	License          string     `json:"license"`
	Stars            int        `json:"stars"`
	AllowedHosts     []string   `json:"allowed_hosts"`
	MinGoblogVersion string     `json:"min_goblog_version"`
	SourceURL        string     `json:"source_url"`
	BuiltAt          time.Time  `json:"built_at"`
	LastAttemptAt    *time.Time `json:"last_attempt_at"`
	LastError        string     `json:"last_error"`
}

// Submit is the public path: parse, refuse duplicates, rate-limit, validate
// and build synchronously, then queue the repository for review.
func (s *Service) Submit(ctx context.Context, input, ip, token string) (*Repo, error) {
	repo, err := ParseRepo(input)
	if err != nil {
		return nil, err
	}
	if err := s.refuseDuplicate(repo, true); err != nil {
		return nil, err
	}
	if !s.limiter.Allow(ip) {
		return nil, ErrRateLimited
	}
	if !s.validate.TryLock() {
		return nil, ErrBusy
	}
	defer s.validate.Unlock()
	ctx, cancel := context.WithTimeout(ctx, submitBudget)
	defer cancel()
	doc, err := s.build(ctx, repo, token)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	return s.save(repo, doc, StatusPending, ip)
}

// Add is the admin path: same validation, no rate limit, listed at once.
func (s *Service) Add(ctx context.Context, input, token string) (*Repo, error) {
	repo, err := ParseRepo(input)
	if err != nil {
		return nil, err
	}
	if err := s.refuseDuplicate(repo, false); err != nil {
		return nil, err
	}
	s.validate.Lock()
	defer s.validate.Unlock()
	doc, err := s.build(ctx, repo, token)
	if err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	r, err := s.save(repo, doc, StatusApproved, "")
	if err != nil {
		return nil, err
	}
	return r, s.regenerate()
}

// refuseDuplicate rejects a repository that is already approved, and (for
// public submissions) one that is already waiting. Rejected ones may be
// submitted again.
func (s *Service) refuseDuplicate(repo string, pendingToo bool) error {
	var existing Repo
	err := s.db.Where("repo = ?", repo).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch existing.Status {
	case StatusApproved:
		return ErrAlreadyListed
	case StatusPending:
		if pendingToo {
			return ErrUnderReview
		}
	}
	return nil
}

func (s *Service) build(ctx context.Context, repo, token string) (registry.DetailDoc, error) {
	return registry.BuildRepo(ctx, s.newSource(token), s.validator, repo, s.siteURL())
}

// save records a successful build in one transaction: the Repo row is
// created or reset to status, and its Build row is created or replaced. A
// plugin name already published by another repository is refused because
// the directory routes on names.
func (s *Service) save(repo string, doc registry.DetailDoc, status, ip string) (*Repo, error) {
	var out Repo
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var taken Build
		err := tx.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
			Where("directory_builds.name = ? AND directory_repos.repo <> ?", doc.Name, repo).First(&taken).Error
		if err == nil {
			return ErrNameTaken
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var r Repo
		if err := tx.Where("repo = ?", repo).First(&r).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now()
		r.Repo, r.Status, r.SubmittedAt, r.SubmitterIP, r.RejectReason, r.DecidedAt = repo, status, now, ip, "", nil
		if status == StatusApproved {
			r.DecidedAt = &now
		}
		if err := tx.Save(&r).Error; err != nil {
			return err
		}

		var b Build
		if err := tx.Where("repo_id = ?", r.ID).First(&b).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		b.RepoID = r.ID
		b.LastAttemptAt = &now
		if err := b.SetDoc(doc); err != nil {
			return err
		}
		if err := tx.Save(&b).Error; err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Approve lists a pending or rejected repository.
func (s *Service) Approve(id uint) error { return s.decide(id, StatusApproved, "") }

// Reject takes a repository off the directory (or keeps it off) with a
// reason only the admin sees.
func (s *Service) Reject(id uint, reason string) error { return s.decide(id, StatusRejected, reason) }

func (s *Service) decide(id uint, status, reason string) error {
	res := s.db.Model(&Repo{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "decided_at": time.Now(), "reject_reason": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return s.regenerate()
}

// Delist forgets a repository entirely; it can be submitted again later.
func (s *Service) Delist(id uint) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&Repo{}, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return tx.Where("repo_id = ?", id).Delete(&Build{}).Error
	})
	if err != nil {
		return err
	}
	return s.regenerate()
}

// Rebuild re-validates one repository now (admin action).
func (s *Service) Rebuild(ctx context.Context, id uint, token string) error {
	var r Repo
	if err := s.db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.rebuildRepo(ctx, r, token, false); err != nil {
		return err
	}
	return s.regenerate()
}

// rebuildRepo refreshes one repository's build. With skipUnchanged, a
// repository whose latest release is already built only gets its star
// count refreshed (two API calls instead of a full download). Any failure
// is recorded on the build row and returned; the previous document stays
// so a broken release never takes a plugin off the directory.
func (s *Service) rebuildRepo(ctx context.Context, r Repo, token string, skipUnchanged bool) error {
	var b Build
	if err := s.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil {
		return err
	}
	src := s.newSource(token)
	now := time.Now()
	b.LastAttemptAt = &now

	s.validate.Lock()
	err := func() error {
		if skipUnchanged {
			v, err := registry.LatestVersion(ctx, src, r.Repo)
			if err != nil {
				return err
			}
			if v == b.Version {
				owner, name, _ := strings.Cut(r.Repo, "/")
				stars, err := src.RepoStars(ctx, owner, name)
				if err != nil {
					return err
				}
				d, err := b.Detail()
				if err != nil {
					return err
				}
				d.Stars = stars
				builtAt := b.BuiltAt
				if err := b.SetDoc(d); err != nil {
					return err
				}
				b.BuiltAt = builtAt // the document itself is unchanged
				return nil
			}
		}
		doc, err := s.build(ctx, r.Repo, token)
		if err != nil {
			return err
		}
		return b.SetDoc(doc)
	}()
	s.validate.Unlock()

	if err != nil {
		b.LastError = err.Error()
	}
	if saveErr := s.db.Save(&b).Error; saveErr != nil {
		return saveErr
	}
	return err
}

// RefreshAll is the scheduled rebuild of every approved repository. One
// failure does not stop the others; all are returned joined. It also
// blanks submitter addresses older than ipRetention.
func (s *Service) RefreshAll(ctx context.Context, token string) error {
	var repos []Repo
	if err := s.db.Where("status = ?", StatusApproved).Find(&repos).Error; err != nil {
		return err
	}
	var errs []error
	for _, r := range repos {
		if err := s.rebuildRepo(ctx, r, token, true); err != nil {
			log.Printf("Directory plugin: refresh %s: %v", r.Repo, err)
			errs = append(errs, fmt.Errorf("%s: %w", r.Repo, err))
		}
	}
	if err := s.db.Model(&Repo{}).Where("submitter_ip <> '' AND submitted_at < ?", time.Now().Add(-ipRetention)).
		Update("submitter_ip", "").Error; err != nil {
		errs = append(errs, err)
	}
	s.mu.Lock()
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	if err := s.regenerate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// LastRefresh is when RefreshAll last ran (zero if never).
func (s *Service) LastRefresh() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRefresh
}

// List returns repositories (newest submission first), optionally filtered
// by status.
func (s *Service) List(status string) ([]RepoView, error) {
	q := s.db.Order("submitted_at desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var repos []Repo
	if err := q.Find(&repos).Error; err != nil {
		return nil, err
	}
	var builds []Build
	if err := s.db.Find(&builds).Error; err != nil {
		return nil, err
	}
	byRepo := make(map[uint]*Build, len(builds))
	for i := range builds {
		byRepo[builds[i].RepoID] = &builds[i]
	}
	views := make([]RepoView, 0, len(repos))
	for _, r := range repos {
		views = append(views, view(r, byRepo[r.ID]))
	}
	return views, nil
}

// Get returns one repository with its full document (README and all).
func (s *Service) Get(id uint) (RepoView, registry.DetailDoc, error) {
	var r Repo
	if err := s.db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RepoView{}, registry.DetailDoc{}, ErrNotFound
		}
		return RepoView{}, registry.DetailDoc{}, err
	}
	var b Build
	if err := s.db.Where("repo_id = ?", r.ID).First(&b).Error; err != nil {
		return RepoView{}, registry.DetailDoc{}, err
	}
	d, err := b.Detail()
	if err != nil {
		return RepoView{}, registry.DetailDoc{}, err
	}
	return view(r, &b), d, nil
}

func view(r Repo, b *Build) RepoView {
	v := RepoView{ID: r.ID, Repo: r.Repo, Status: r.Status, SubmittedAt: r.SubmittedAt, DecidedAt: r.DecidedAt,
		RejectReason: r.RejectReason, AllowedHosts: []string{}}
	if b == nil {
		return v
	}
	v.BuiltAt, v.LastAttemptAt, v.LastError = b.BuiltAt, b.LastAttemptAt, b.LastError
	if d, err := b.Detail(); err == nil {
		v.Name, v.DisplayName, v.Version, v.Author, v.License = d.Name, d.DisplayName, d.Version, d.Author, d.License
		v.Stars, v.AllowedHosts, v.MinGoblogVersion, v.SourceURL = d.Stars, d.AllowedHosts, d.MinGoblogVersion, d.SourceURL
	}
	return v
}

// Index returns the current index.json bytes and entries, generating them
// on first use.
func (s *Service) Index() ([]byte, []registry.IndexEntry) {
	s.mu.RLock()
	raw, entries := s.indexRaw, s.indexEntries
	s.mu.RUnlock()
	if raw != nil {
		return raw, entries
	}
	if err := s.regenerate(); err != nil {
		log.Printf("Directory plugin: build index: %v", err)
		return []byte("[]\n"), nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexRaw, s.indexEntries
}

// Detail returns the document of an approved plugin by name.
func (s *Service) Detail(name string) (registry.DetailDoc, bool) {
	var b Build
	err := s.db.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
		Where("directory_builds.name = ? AND directory_repos.status = ?", name, StatusApproved).First(&b).Error
	if err != nil {
		return registry.DetailDoc{}, false
	}
	d, err := b.Detail()
	if err != nil {
		log.Printf("Directory plugin: decode %s: %v", name, err)
		return registry.DetailDoc{}, false
	}
	return d, true
}

// regenerate rebuilds the cached index from the approved builds.
func (s *Service) regenerate() error {
	docs, err := approvedDocs(s.db)
	if err != nil {
		return err
	}
	raw, entries := encodeIndex(docs)
	s.mu.Lock()
	s.indexRaw, s.indexEntries = raw, entries
	s.mu.Unlock()
	return nil
}
