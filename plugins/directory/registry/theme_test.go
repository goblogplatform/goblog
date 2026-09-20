package registry

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

const goodThemeManifest = `{
  "name": "ocean",
  "display_name": "Ocean",
  "description": "Blue and calm.",
  "author": "Jason Ernst",
  "license": "MIT",
  "min_goblog_version": "0.5.0",
  "homepage": "https://example.test"
}`

// zipOf builds an archive with the given entries, optionally under a
// top-level folder the way GitHub's tag archives are laid out.
func zipOf(t *testing.T, prefix string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseThemeManifest(t *testing.T) {
	m, err := ParseThemeManifest([]byte(goodThemeManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "ocean" || m.DisplayName != "Ocean" || m.License != "MIT" || m.MinGoblogVersion != "0.5.0" || m.Homepage != "https://example.test" {
		t.Errorf("manifest = %+v", m)
	}
	cases := map[string]string{
		`{"name":"Ocean"}`: "name must match",
		strings.Replace(goodThemeManifest, `"ocean"`, `"default"`, 1):   "reserved",
		strings.Replace(goodThemeManifest, `"ocean"`, `"installed"`, 1): "reserved",
		strings.Replace(goodThemeManifest, `"MIT"`, `"WTFPL"`, 1):       "license",
		strings.Replace(goodThemeManifest, `"0.5.0"`, `"v0.5.0"`, 1):    "min_goblog_version",
		strings.Replace(goodThemeManifest, `"Blue and calm."`, `""`, 1): "description is required",
		`not json`: "goblog-theme.json",
	}
	for in, want := range cases {
		if _, err := ParseThemeManifest([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseThemeManifest(%q): want error containing %q, got %v", in, want, err)
		}
	}
}

func TestParseArchive_StripsPrefixAndFilters(t *testing.T) {
	z := zipOf(t, "ocean-1.0.0/", map[string]string{
		"templates/home.html": "home", "static/css/a.css": "css", "README.md": "readme",
		"goblog-theme.json": goodThemeManifest, "templates/": "", ".github/workflows/x.yml": "ci",
	})
	files, err := ParseArchive(z)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || string(files["templates/home.html"]) != "home" || string(files["static/css/a.css"]) != "css" {
		t.Errorf("files = %v", keys(files))
	}
	// No top-level folder is fine too.
	flat, err := ParseArchive(zipOf(t, "", map[string]string{"templates/home.html": "h"}))
	if err != nil || len(flat) != 1 {
		t.Errorf("flat archive: %v %v", keys(flat), err)
	}
}

func TestParseArchive_Rejects(t *testing.T) {
	cases := map[string][]byte{
		"not a zip":      []byte("nope"),
		"traversal":      zipOf(t, "x/", map[string]string{"templates/../../etc/passwd": "p"}),
		"absolute":       zipOf(t, "", map[string]string{"/templates/home.html": "h"}),
		"nul byte":       zipOf(t, "", map[string]string{"templates/a\x00b": "h"}),
		"backslash":      zipOf(t, "", map[string]string{"templates/..\\..\\x": "h"}),
		"dotdot wrapper": zipOf(t, "../", map[string]string{"templates/home.html": "h"}),
	}
	for name, z := range cases {
		if _, err := ParseArchive(z); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// Too many entries.
	many := map[string]string{}
	for i := 0; i <= MaxArchiveEntries; i++ {
		many["static/"+strings.Repeat("a", i%10)+string(rune('a'+i%26))+strings.Repeat("b", i/26)] = "x"
	}
	if _, err := ParseArchive(zipOf(t, "", many)); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Errorf("entry cap: %v", err)
	}
	// Symlink entry.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "static/link"}
	hdr.SetMode(fs.ModeSymlink | 0o777)
	f, _ := w.CreateHeader(hdr)
	f.Write([]byte("templates/home.html"))
	w.Close()
	if _, err := ParseArchive(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: %v", err)
	}
	// Duplicate entry names (same name reachable twice, e.g. after prefix
	// stripping) must be refused rather than silently taking the last one.
	var dupBuf bytes.Buffer
	dw := zip.NewWriter(&dupBuf)
	d1, _ := dw.Create("templates/home.html")
	d1.Write([]byte("a"))
	d2, _ := dw.Create("templates/home.html")
	d2.Write([]byte("b"))
	dw.Close()
	if _, err := ParseArchive(dupBuf.Bytes()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate: %v", err)
	}
	// A single stored (uncompressed) entry bigger than MaxAssetBytes makes
	// the archive itself exceed MaxAssetBytes.
	var hugeBuf bytes.Buffer
	hw := zip.NewWriter(&hugeBuf)
	hh, _ := hw.CreateHeader(&zip.FileHeader{Name: "static/huge.bin", Method: zip.Store})
	hh.Write(make([]byte, MaxAssetBytes+1))
	hw.Close()
	if _, err := ParseArchive(hugeBuf.Bytes()); err == nil {
		t.Error("over-cap archive: expected an error")
	}
	// Two entries that individually fit under MaxAssetBytes but together
	// exceed it once extracted. They compress well (all zero bytes), so the
	// archive itself stays comfortably under MaxAssetBytes and this only
	// trips the running extracted-size total, not the archive-size check.
	var totalBuf bytes.Buffer
	tw := zip.NewWriter(&totalBuf)
	big := make([]byte, 9<<20) // 9 MiB
	for _, name := range []string{"static/big1.bin", "static/big2.bin"} {
		tf, err := tw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		tf.Write(big)
	}
	tw.Close()
	if _, err := ParseArchive(totalBuf.Bytes()); err == nil || !strings.Contains(err.Error(), "extracted size") {
		t.Errorf("extracted size cap: %v", err)
	}
}

func TestContentHash(t *testing.T) {
	a := map[string][]byte{"templates/home.html": []byte("h"), "static/a.css": []byte("c")}
	b := map[string][]byte{"static/a.css": []byte("c"), "templates/home.html": []byte("h")}
	if ContentHash(a) != ContentHash(b) {
		t.Error("hash must not depend on map order")
	}
	c := map[string][]byte{"templates/home.html": []byte("h"), "static/a.css": []byte("d")}
	if ContentHash(a) == ContentHash(c) {
		t.Error("hash must change with content")
	}
	// Length is part of the input, so moving a byte across a boundary changes it.
	d := map[string][]byte{"templates/home.html": []byte("hc"), "static/a.css": []byte("")}
	if ContentHash(a) == ContentHash(d) {
		t.Error("hash must bind bytes to their file")
	}
	if len(ContentHash(a)) != 64 {
		t.Errorf("hex sha256 expected, got %q", ContentHash(a))
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
