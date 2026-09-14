package plugin_test

import (
	"goblog/plugin"
	"os"
	"path/filepath"
	"testing"
)

// TestValidate_ShippedExample checks the example dynamic plugin validates
// and reports the identity the registry CI will compare against a manifest.
func TestValidate_ShippedExample(t *testing.T) {
	info, err := plugin.Validate("../plugins/dynamic/hello.go.example")
	if err != nil {
		t.Fatalf("validate shipped example: %v", err)
	}
	if info.Name != "hello" || info.DisplayName != "Hello (example)" || info.Version != "1.0.0" {
		t.Errorf("unexpected info: %+v", info)
	}
}

// TestValidate_Errors covers the ways a submitted file can be unusable.
func TestValidate_Errors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := map[string]string{
		"does not compile":    write("broken.go", "package main\nfunc NewPlugin() plugin.Plugin { return nil "),
		"no NewPlugin":        write("noctor.go", "package main\nfunc Other() int { return 1 }\n"),
		"empty Name()":        write("noname.go", "package main\nimport \"goblog/plugin\"\ntype P struct{ plugin.BasePlugin }\nfunc NewPlugin() plugin.Plugin { return &P{} }\nfunc (P) Name() string { return \"\" }\nfunc (P) DisplayName() string { return \"x\" }\nfunc (P) Version() string { return \"1\" }\n"),
		"empty Version()":     write("noversion.go", "package main\nimport \"goblog/plugin\"\ntype P struct{ plugin.BasePlugin }\nfunc NewPlugin() plugin.Plugin { return &P{} }\nfunc (P) Name() string { return \"p\" }\nfunc (P) DisplayName() string { return \"x\" }\nfunc (P) Version() string { return \"\" }\n"),
		"missing file":        filepath.Join(dir, "missing.go"),
	}
	for name, path := range cases {
		if _, err := plugin.Validate(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
