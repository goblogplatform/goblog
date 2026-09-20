// Package registry validates goblog plugin repositories and builds the
// directory's index and detail documents. It is what the goblogplatform/
// plugins registry tool used to do in CI; goblog.live now does it itself
// from the directory plugin.
package registry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Manifest is goblog-plugin.json at the root of a plugin repository.
type Manifest struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description"`
	Author           string   `json:"author"`
	License          string   `json:"license"`
	Runtime          string   `json:"runtime"`
	Entry            string   `json:"entry"`
	AllowedHosts     []string `json:"allowed_hosts"`
	MinGoblogVersion string   `json:"min_goblog_version"`
	Homepage         string   `json:"homepage"`
}

// NamePattern is the rule for plugin names; the directory routes on it.
var NamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// entryPattern is the rule for the manifest's entry: the name of a .wasm
// asset attached to each release, with no path separators or odd characters.
var entryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.wasm$`)

// hostPattern is the rule for one allowed_hosts entry: a hostname, IP or
// glob (goblog matches them with github.com/gobwas/glob), optionally with a
// port — never a scheme or a path.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.*:-]+$`)

// wildcardOnly matches an allowed_hosts entry made of nothing but `*` and
// `.` (`*`, `**`, `*.*`): a glob that names no host at all and would let the
// plugin reach anything. The registry is the curation point, so it refuses
// them rather than leaving it to the operator.
var wildcardOnly = regexp.MustCompile(`^[*.]+$`)

// versionPattern is the min_goblog_version rule: plain semver, three
// numbers, no leading v and no pre-release suffix.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// knownLicenses is the set of SPDX identifiers accepted in a manifest. It is
// deliberately short; add to it when a submission needs another one.
var knownLicenses = map[string]bool{
	"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true,
	"ISC": true, "MPL-2.0": true, "Unlicense": true, "0BSD": true,
	"GPL-2.0-only": true, "GPL-2.0-or-later": true, "GPL-3.0-only": true, "GPL-3.0-or-later": true,
	"LGPL-2.1-only": true, "LGPL-2.1-or-later": true, "LGPL-3.0-only": true, "LGPL-3.0-or-later": true,
	"AGPL-3.0-only": true, "AGPL-3.0-or-later": true,
}

// KnownLicenses returns the accepted SPDX identifiers, sorted; the docs
// tests check the published lists against it.
func KnownLicenses() []string {
	out := make([]string, 0, len(knownLicenses))
	for l := range knownLicenses {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// ParseManifest decodes and validates a manifest. Entry defaults to
// plugin.wasm; AllowedHosts is never nil on success so the index serialises
// it as [] rather than null.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("goblog-plugin.json: %w", err)
	}
	if m.Entry == "" {
		m.Entry = "plugin.wasm"
	}
	if m.AllowedHosts == nil {
		m.AllowedHosts = []string{}
	}
	var problems []string
	if !NamePattern.MatchString(m.Name) {
		problems = append(problems, "name must match ^[a-z0-9-]+$")
	}
	for field, v := range map[string]string{"display_name": m.DisplayName, "description": m.Description, "author": m.Author} {
		if strings.TrimSpace(v) == "" {
			problems = append(problems, field+" is required")
		}
	}
	if !knownLicenses[m.License] {
		problems = append(problems, fmt.Sprintf("license %q is not a known SPDX identifier", m.License))
	}
	if m.Runtime != "wasm" {
		problems = append(problems, "runtime must be \"wasm\": the directory only lists WebAssembly plugins; see /docs/publishing-a-plugin")
	}
	if !entryPattern.MatchString(m.Entry) {
		problems = append(problems, "entry must be a .wasm release asset name (letters, digits, `_`, `.`, `-`)")
	}
	for _, h := range m.AllowedHosts {
		if h == "" || !hostPattern.MatchString(h) {
			problems = append(problems, "allowed_hosts entries must be hostnames, IPs or globs without scheme or path")
			break
		}
		if wildcardOnly.MatchString(h) {
			problems = append(problems, `allowed_hosts entries must name a host; "*" alone is not allowed`)
			break
		}
	}
	if !versionPattern.MatchString(m.MinGoblogVersion) {
		problems = append(problems, "min_goblog_version must be a plain semver like 0.2.6")
	}
	if len(problems) > 0 {
		return Manifest{}, fmt.Errorf("goblog-plugin.json: %s", strings.Join(problems, "; "))
	}
	return m, nil
}
