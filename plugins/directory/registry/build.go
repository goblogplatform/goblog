package registry

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"
)

// IndexEntry is one element of index.json: the latest release of a plugin.
// Field names are the contract consumed by goblog's directory plugin and
// the admin installer; do not rename them.
type IndexEntry struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description"`
	Version          string   `json:"version"`
	Author           string   `json:"author"`
	License          string   `json:"license"`
	SourceURL        string   `json:"source_url"`
	DownloadURL      string   `json:"download_url"`
	SHA256           string   `json:"sha256"`
	MinGoblogVersion string   `json:"min_goblog_version"`
	InstallType      string   `json:"install_type"`  // "wasm"
	Runtime          string   `json:"runtime"`       // "wasm"
	AllowedHosts     []string `json:"allowed_hosts"` // never null: [] when the plugin uses no network
	ReleasedAt       string   `json:"released_at"`   // RFC 3339
	DetailURL        string   `json:"detail_url"`
	Stars            int      `json:"stars"`
}

// ReleaseDoc is one release in a plugin's history.
type ReleaseDoc struct {
	Version    string        `json:"version"`
	ReleasedAt string        `json:"released_at"`
	NotesHTML  template.HTML `json:"notes_html"` // rendered and sanitized by GitHub's markdown API
	URL        string        `json:"url"`
}

// MaxRenderedBytes caps the README and changelog HTML BuildRepo will
// accept from GitHub's markdown API — a detail doc is stored whole in
// directory_builds.doc and served on every page view, so an oversized
// render (an author's README with an embedded base64 image, say) must be
// refused at build time rather than silently ballooning the database and
// every response.
const MaxRenderedBytes = 1 << 20

// DetailDoc is /plugins/<name>.json: the index entry plus rendered README,
// changelog and release history. The *_html fields come from GitHub's
// markdown API, which sanitizes them; they are the only fields pages insert
// unescaped.
type DetailDoc struct {
	IndexEntry
	ReadmeHTML    template.HTML `json:"readme_html"`
	ChangelogHTML template.HTML `json:"changelog_html"`
	Releases      []ReleaseDoc  `json:"releases"`
}

// BuildRepo validates repo end to end and returns the document the
// directory publishes for it. baseURL is the site's URL ("" makes
// detail_url relative).
func BuildRepo(ctx context.Context, src Source, val Validator, repo, baseURL string) (DetailDoc, error) {
	v, err := ValidateEntry(ctx, src, val, repo)
	if err != nil {
		return DetailDoc{}, err
	}
	return buildDetail(ctx, src, v, baseURL)
}

func buildDetail(ctx context.Context, src Source, v *Validated, baseURL string) (DetailDoc, error) {
	ownerRepo := v.Owner + "/" + v.Name
	entry := IndexEntry{
		Name:             v.Manifest.Name,
		DisplayName:      v.Manifest.DisplayName,
		Description:      v.Manifest.Description,
		Version:          v.Version,
		Author:           v.Manifest.Author,
		License:          v.Manifest.License,
		SourceURL:        "https://github.com/" + ownerRepo,
		DownloadURL:      v.Asset.DownloadURL,
		SHA256:           v.SHA256,
		MinGoblogVersion: v.Manifest.MinGoblogVersion,
		InstallType:      "wasm",
		Runtime:          "wasm",
		AllowedHosts:     v.Manifest.AllowedHosts,
		ReleasedAt:       v.Release.PublishedAt.UTC().Format(time.RFC3339),
		DetailURL:        strings.TrimSuffix(baseURL, "/") + "/plugins/" + v.Manifest.Name + ".json",
	}

	// Stars only order the directory; a failed lookup must not drop an
	// otherwise valid plugin.
	if stars, err := src.RepoStars(ctx, v.Owner, v.Name); err != nil {
		log.Printf("%s: stars unavailable, using 0: %v", ownerRepo, err)
	} else {
		entry.Stars = stars
	}

	readme, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "README.md")
	if err != nil {
		return DetailDoc{}, fmt.Errorf("README.md: %w", err)
	}
	readmeHTML, err := src.RenderMarkdown(ctx, ownerRepo, string(readme))
	if err != nil {
		return DetailDoc{}, err
	}
	if len(readmeHTML) > MaxRenderedBytes {
		return DetailDoc{}, fmt.Errorf("%s: rendered README.md is %d bytes; the limit is %d (1 MiB)", ownerRepo, len(readmeHTML), MaxRenderedBytes)
	}
	changelogHTML := ""
	if cl, err := src.File(ctx, v.Owner, v.Name, v.Release.Tag, "CHANGELOG.md"); err == nil {
		if changelogHTML, err = src.RenderMarkdown(ctx, ownerRepo, string(cl)); err != nil {
			return DetailDoc{}, err
		}
		if len(changelogHTML) > MaxRenderedBytes {
			return DetailDoc{}, fmt.Errorf("%s: rendered CHANGELOG.md is %d bytes; the limit is %d (1 MiB)", ownerRepo, len(changelogHTML), MaxRenderedBytes)
		}
	} else if !isNotFound(err) {
		return DetailDoc{}, fmt.Errorf("CHANGELOG.md: %w", err)
	}

	releases := make([]ReleaseDoc, 0, len(v.Releases))
	for _, r := range v.Releases {
		notes, err := src.RenderMarkdown(ctx, ownerRepo, r.Body)
		if err != nil {
			return DetailDoc{}, err
		}
		releases = append(releases, ReleaseDoc{
			Version:    strings.TrimPrefix(r.Tag, "v"),
			ReleasedAt: r.PublishedAt.UTC().Format(time.RFC3339),
			NotesHTML:  template.HTML(notes),
			URL:        r.URL,
		})
	}
	return DetailDoc{IndexEntry: entry, ReadmeHTML: template.HTML(readmeHTML), ChangelogHTML: template.HTML(changelogHTML), Releases: releases}, nil
}
