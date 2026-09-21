package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TagPattern is the release tag rule: vX.Y.Z, nothing else.
var TagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Validated is a plugin repository that passed every check, with everything
// the index builder needs from it.
type Validated struct {
	Repo     string // owner/name
	Owner    string
	Name     string // repository name (not the plugin name)
	Manifest Manifest
	Release  Release   // the latest published, non-prerelease release
	Version  string    // Release.Tag without the leading v
	Releases []Release // all published, non-prerelease releases, newest first
	Readme   []byte    // README.md at Release.Tag, kept so the builder need not fetch it again
	Asset    Asset     // the release asset named by Manifest.Entry
	Entry    []byte    // the asset's bytes (the WebAssembly module)
	SHA256   string    // hex sha256 of Entry
}

// ValidateEntry checks one registry entry end to end: a published release
// tagged vX.Y.Z, a valid manifest and README at that tag, a release asset
// named by the manifest's entry that loads in goblog as a WebAssembly
// plugin, and an identity that matches the manifest and the tag.
func ValidateEntry(ctx context.Context, src Source, val Validator, repo string) (*Validated, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%q: repo must be owner/name", repo)
	}

	latest, releases, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(latest.Tag, "v")

	mb, err := src.File(ctx, owner, name, latest.Tag, "goblog-plugin.json")
	if err != nil {
		return nil, fmt.Errorf("%s@%s: goblog-plugin.json: %w", repo, latest.Tag, err)
	}
	manifest, err := ParseManifest(mb)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %w", repo, latest.Tag, err)
	}
	readme, err := src.File(ctx, owner, name, latest.Tag, "README.md")
	if err != nil {
		return nil, fmt.Errorf("%s@%s: README.md: %w", repo, latest.Tag, err)
	}
	var asset *Asset
	for i := range latest.Assets {
		if latest.Assets[i].Name == manifest.Entry {
			asset = &latest.Assets[i]
			break
		}
	}
	if asset == nil {
		return nil, fmt.Errorf("%s: release %s has no asset named %s (the release workflow must upload it)", repo, latest.Tag, manifest.Entry)
	}
	if asset.Size > MaxAssetBytes {
		return nil, fmt.Errorf("%s@%s: asset %s is %d bytes; the limit is %d (16 MiB)", repo, latest.Tag, asset.Name, asset.Size, MaxAssetBytes)
	}
	entry, err := src.ReleaseAsset(ctx, owner, name, asset.ID)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: asset %s: %w", repo, latest.Tag, asset.Name, err)
	}

	info, err := val.Validate(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("%s@%s: %s does not load: %w", repo, latest.Tag, manifest.Entry, err)
	}
	if info.Runtime != "wasm" {
		return nil, fmt.Errorf("%s@%s: %s is not a WebAssembly plugin (runtime %q)", repo, latest.Tag, manifest.Entry, info.Runtime)
	}
	if info.Name != manifest.Name {
		return nil, fmt.Errorf("%s@%s: Name() is %q but the manifest says %q", repo, latest.Tag, info.Name, manifest.Name)
	}
	if info.Version != version {
		return nil, fmt.Errorf("%s@%s: Version() is %q but the release tag says %q", repo, latest.Tag, info.Version, version)
	}

	h := sha256.Sum256(entry)
	return &Validated{
		Repo: repo, Owner: owner, Name: name,
		Manifest: manifest, Release: latest, Version: version, Releases: releases, Readme: readme,
		Asset: *asset, Entry: entry, SHA256: hex.EncodeToString(h[:]),
	}, nil
}

// latestRelease picks the newest published, non-prerelease release (by
// date, not GitHub's order) and the vX.Y.Z-tagged history shown to readers.
// The tag check runs on the newest release before filtering, so a bad tag
// on the latest release is an error rather than silently skipped.
func latestRelease(ctx context.Context, src Source, repo, owner, name string) (Release, []Release, error) {
	all, err := src.Releases(ctx, owner, name)
	if err != nil {
		return Release{}, nil, err
	}
	var releases []Release
	for _, r := range all {
		if !r.Draft && !r.Prerelease {
			releases = append(releases, r)
		}
	}
	if len(releases) == 0 {
		return Release{}, nil, fmt.Errorf("%s: no published release (drafts and pre-releases are ignored)", repo)
	}
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].PublishedAt.After(releases[j].PublishedAt) })
	latest := releases[0]
	if !TagPattern.MatchString(latest.Tag) {
		return Release{}, nil, fmt.Errorf("%s: release tag %q must be vX.Y.Z", repo, latest.Tag)
	}
	filtered := releases[:0:0]
	for _, r := range releases {
		if TagPattern.MatchString(r.Tag) {
			filtered = append(filtered, r)
		}
	}
	return latest, filtered, nil
}

// LatestVersion is the version ValidateEntry would publish for repo, found
// without downloading anything. The scheduled rebuild uses it to skip repos
// whose latest release is already built.
func LatestVersion(ctx context.Context, src Source, repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("%q: repo must be owner/name", repo)
	}
	latest, _, err := latestRelease(ctx, src, repo, owner, name)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(latest.Tag, "v"), nil
}

// isNotFound reports whether err is a missing-file error from a Source.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
