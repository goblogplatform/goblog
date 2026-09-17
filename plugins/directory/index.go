package directory

import "html/template"

// Entry is one plugin in index.json: the latest release of a plugin as
// published by the registry (github.com/goblogplatform/plugins).
type Entry struct {
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Version          string `json:"version"`
	Author           string `json:"author"`
	License          string `json:"license"`
	SourceURL        string `json:"source_url"`
	DownloadURL      string `json:"download_url"`
	SHA256           string `json:"sha256"`
	MinGoblogVersion string `json:"min_goblog_version"`
	InstallType      string `json:"install_type"`
	ReleasedAt       string `json:"released_at"` // RFC 3339; shown as-is, never parsed
	DetailURL        string `json:"detail_url"`
	Stars            int    `json:"stars"` // GitHub stargazers, added by the registry build
}

// Release is one entry of a plugin's release history.
type Release struct {
	Version    string        `json:"version"`
	ReleasedAt string        `json:"released_at"`
	NotesHTML  template.HTML `json:"notes_html"` // rendered and sanitized by the registry build
	URL        string        `json:"url"`
}

// Detail is plugins/<name>.json: the index entry plus the README, changelog
// and release history. The *_html fields are rendered through GitHub's
// markdown API by the registry build, which sanitizes them; they are the
// only fields inserted into pages unescaped.
type Detail struct {
	Entry
	ReadmeHTML    template.HTML `json:"readme_html"`
	ChangelogHTML template.HTML `json:"changelog_html"`
	Releases      []Release     `json:"releases"`
}
