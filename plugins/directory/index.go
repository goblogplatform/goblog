package directory

import "goblog/plugins/directory/registry"

// Entry, Detail and Release are the wire types of index.json and
// /plugins/<name>.json. The registry package builds them; the installer
// (in every goblog) decodes them. They are aliases so both sides share one
// definition.
type (
	Entry   = registry.IndexEntry
	Detail  = registry.DetailDoc
	Release = registry.ReleaseDoc
)
