package registry

import (
	"strings"
	"testing"
)

const goodManifest = `{
  "name": "hello",
  "display_name": "Hello",
  "description": "Says hi.",
  "author": "Jason Ernst",
  "license": "Apache-2.0",
  "runtime": "wasm",
  "entry": "plugin.wasm",
  "allowed_hosts": ["api.example.test"],
  "min_goblog_version": "0.2.6",
  "homepage": "https://example.test"
}`

func TestParseManifest_Good(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hello" || m.DisplayName != "Hello" || m.License != "Apache-2.0" || m.Runtime != "wasm" || m.Entry != "plugin.wasm" || m.MinGoblogVersion != "0.2.6" || m.Homepage != "https://example.test" {
		t.Errorf("unexpected manifest: %+v", m)
	}
	if len(m.AllowedHosts) != 1 || m.AllowedHosts[0] != "api.example.test" {
		t.Errorf("allowed_hosts = %v", m.AllowedHosts)
	}
}

func TestParseManifest_EntryDefaultsToPluginWasm(t *testing.T) {
	m, err := ParseManifest([]byte(strings.Replace(goodManifest, `"entry": "plugin.wasm",`, "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Entry != "plugin.wasm" {
		t.Errorf("entry should default to plugin.wasm, got %q", m.Entry)
	}
}

// TestParseManifest_AllowedHostsNeverNil: a manifest without allowed_hosts
// must parse to an empty slice, so the index serialises "allowed_hosts": []
// rather than null.
func TestParseManifest_AllowedHostsNeverNil(t *testing.T) {
	m, err := ParseManifest([]byte(strings.Replace(goodManifest, `"allowed_hosts": ["api.example.test"],`, "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.AllowedHosts == nil || len(m.AllowedHosts) != 0 {
		t.Errorf("allowed_hosts should default to an empty, non-nil slice, got %#v", m.AllowedHosts)
	}
}

// TestParseManifest_HostGlobs: a glob that still names a domain is fine; one
// made only of wildcards would let the plugin talk to anything and is refused
// with a message that says so.
func TestParseManifest_HostGlobs(t *testing.T) {
	m, err := ParseManifest([]byte(strings.Replace(goodManifest, `["api.example.test"]`, `["*.example.test", "10.0.0.1:8443", "*-cdn.example.test"]`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AllowedHosts) != 3 || m.AllowedHosts[0] != "*.example.test" {
		t.Errorf("allowed_hosts = %v", m.AllowedHosts)
	}
	_, err = ParseManifest([]byte(strings.Replace(goodManifest, `["api.example.test"]`, `["*"]`, 1)))
	if err == nil || !strings.Contains(err.Error(), `"*" alone is not allowed`) {
		t.Errorf("a bare wildcard should be refused with a message naming it, got %v", err)
	}
}

func TestParseManifest_MissingRuntimeMentionsWebAssembly(t *testing.T) {
	_, err := ParseManifest([]byte(strings.Replace(goodManifest, `"runtime": "wasm",`, "", 1)))
	if err == nil || !strings.Contains(err.Error(), "WebAssembly") {
		t.Errorf("a manifest without runtime should be rejected with a pointer to WebAssembly, got %v", err)
	}
}

func TestParseManifest_Errors(t *testing.T) {
	cases := map[string]string{
		"not json":              `{`,
		"missing name":          strings.Replace(goodManifest, `"name": "hello",`, "", 1),
		"bad name":              strings.Replace(goodManifest, `"name": "hello"`, `"name": "Hello_World"`, 1),
		"missing display":       strings.Replace(goodManifest, `"display_name": "Hello",`, "", 1),
		"missing description":   strings.Replace(goodManifest, `"description": "Says hi.",`, "", 1),
		"missing author":        strings.Replace(goodManifest, `"author": "Jason Ernst",`, "", 1),
		"unknown license":       strings.Replace(goodManifest, `"Apache-2.0"`, `"MyLicense"`, 1),
		"missing runtime":       strings.Replace(goodManifest, `"runtime": "wasm",`, "", 1),
		"go runtime":            strings.Replace(goodManifest, `"runtime": "wasm"`, `"runtime": "go"`, 1),
		"entry not wasm":        strings.Replace(goodManifest, `"plugin.wasm"`, `"plugin.go"`, 1),
		"entry with slash":      strings.Replace(goodManifest, `"plugin.wasm"`, `"src/plugin.wasm"`, 1),
		"entry with query char": strings.Replace(goodManifest, `"plugin.wasm"`, `"a?b.wasm"`, 1),
		"entry with space":      strings.Replace(goodManifest, `"plugin.wasm"`, `"a b.wasm"`, 1),
		"bad host":              strings.Replace(goodManifest, `["api.example.test"]`, `["https://x"]`, 1),
		"wildcard host":         strings.Replace(goodManifest, `["api.example.test"]`, `["*"]`, 1),
		"double wildcard host":  strings.Replace(goodManifest, `["api.example.test"]`, `["**"]`, 1),
		"dotted wildcard host":  strings.Replace(goodManifest, `["api.example.test"]`, `["*.*"]`, 1),
		"wildcard among hosts":  strings.Replace(goodManifest, `["api.example.test"]`, `["api.example.test", "*"]`, 1),
		"host with path":        strings.Replace(goodManifest, `["api.example.test"]`, `["x/api"]`, 1),
		"empty host":            strings.Replace(goodManifest, `["api.example.test"]`, `[""]`, 1),
		"min version with v":    strings.Replace(goodManifest, `"0.2.6"`, `"v0.2.6"`, 1),
		"min version junk":      strings.Replace(goodManifest, `"0.2.6"`, `"latest"`, 1),
		"missing min version":   strings.Replace(goodManifest, `"min_goblog_version": "0.2.6",`, "", 1),
	}
	for name, src := range cases {
		if _, err := ParseManifest([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
