package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunValidatePlugin(t *testing.T) {
	var out, errOut bytes.Buffer

	// Happy path: JSON identity on stdout, exit 0.
	if code := runValidatePlugin([]string{"plugins/dynamic/hello.go.example"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	var info map[string]string
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("stdout is not JSON: %v: %q", err, out.String())
	}
	if info["name"] != "hello" || info["display_name"] != "Hello (example)" || info["version"] != "1.0.0" {
		t.Errorf("unexpected identity: %v", info)
	}

	// A file that does not load: message on stderr, exit 1, nothing on stdout.
	out.Reset()
	errOut.Reset()
	if code := runValidatePlugin([]string{"does-not-exist.go"}, &out, &errOut); code != 1 {
		t.Errorf("expected exit 1 for a missing file, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid plugin") || out.Len() != 0 {
		t.Errorf("expected error on stderr only, stdout=%q stderr=%q", out.String(), errOut.String())
	}

	// Usage error: exit 2.
	errOut.Reset()
	if code := runValidatePlugin(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Errorf("expected usage error and exit 2, got %d %q", code, errOut.String())
	}
}
