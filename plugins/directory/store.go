package directory

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"goblog/plugins/directory/registry"

	"gorm.io/gorm"
)

// Submission states of a repository in the directory.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Repo is one GitHub repository that was ever submitted to this directory:
// the curation record. Its build (what gets published) is a separate row
// because the two change on different schedules.
type Repo struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Repo         string     `gorm:"uniqueIndex;size:255" json:"repo"` // owner/name, lower-case
	Status       string     `gorm:"index;size:16" json:"status"`
	SubmittedAt  time.Time  `json:"submitted_at"`
	DecidedAt    *time.Time `json:"decided_at"`
	RejectReason string     `json:"reject_reason"`
	// SubmitterIP is kept for the admin's benefit and blanked by the
	// scheduled job after 7 days.
	SubmitterIP string `gorm:"size:64" json:"-"`
}

func (Repo) TableName() string { return "directory_repos" }

// Build is the last successful build of a repository: the detail document
// as JSON plus the columns the directory sorts and routes on. A failed
// rebuild leaves Doc alone and records LastError, so one broken release
// never takes a plugin off the directory.
type Build struct {
	ID      uint   `gorm:"primaryKey"`
	RepoID  uint   `gorm:"uniqueIndex"`
	Name    string `gorm:"uniqueIndex;size:128"` // plugin name; routes /plugins/<name>
	Version string `gorm:"size:32"`
	Stars   int
	// No type tag: gorm's default column for a Go string is already
	// unbounded (longtext on MySQL, text on sqlite/postgres). A fixed
	// "text" tag would cap this at MySQL's 64 KiB text limit, which a
	// detail doc (README + changelog + per-release notes HTML) can exceed.
	Doc           string // registry.DetailDoc as JSON
	BuiltAt       time.Time
	LastAttemptAt *time.Time
	LastError     string
}

func (Build) TableName() string { return "directory_builds" }

// Migrate creates or updates the directory's tables.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&Repo{}, &Build{})
}

// Detail decodes the stored document.
func (b *Build) Detail() (registry.DetailDoc, error) {
	var d registry.DetailDoc
	err := json.Unmarshal([]byte(b.Doc), &d)
	if d.AllowedHosts == nil {
		d.AllowedHosts = []string{}
	}
	return d, err
}

// SetDoc stores d as this build: the indexed columns, the JSON, the build
// time, and a cleared error.
func (b *Build) SetDoc(d registry.DetailDoc) error {
	raw, err := marshalJSON(d)
	if err != nil {
		return err
	}
	b.Name, b.Version, b.Stars, b.Doc = d.Name, d.Version, d.Stars, string(raw)
	b.BuiltAt = time.Now()
	b.LastError = ""
	return nil
}

// approvedDocs returns the documents of every approved repository, sorted
// by plugin name — the content of index.json.
func approvedDocs(db *gorm.DB) ([]registry.DetailDoc, error) {
	var builds []Build
	err := db.Joins("JOIN directory_repos ON directory_repos.id = directory_builds.repo_id").
		Where("directory_repos.status = ?", StatusApproved).Find(&builds).Error
	if err != nil {
		return nil, err
	}
	docs := make([]registry.DetailDoc, 0, len(builds))
	for _, b := range builds {
		d, err := b.Detail()
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

// encodeIndex renders index.json ([] when empty) and the entries it holds.
func encodeIndex(docs []registry.DetailDoc) ([]byte, []registry.IndexEntry) {
	entries := make([]registry.IndexEntry, 0, len(docs))
	for _, d := range docs {
		entries = append(entries, d.IndexEntry)
	}
	raw, _ := marshalJSON(entries)
	return raw, entries
}

// marshalJSON encodes without HTML escaping (the docs carry HTML) and with
// the indentation the old GitHub Pages index had.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
