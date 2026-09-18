package wasm

import (
	"os"
	"path/filepath"
	"testing"

	"goblog/plugin"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadWasmPlugins(t *testing.T) {
	dir := t.TempDir()
	src := echoBytes(t)
	os.WriteFile(filepath.Join(dir, "echo.wasm"), src, 0644)
	os.WriteFile(filepath.Join(dir, "broken.wasm"), []byte("nope"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644)
	os.Mkdir(filepath.Join(dir, "sub"), 0755)
	if err := WriteSidecar(filepath.Join(dir, "echo.wasm"), []string{"api.example.test"}); err != nil {
		t.Fatal(err)
	}
	if SidecarPath(filepath.Join(dir, "echo.wasm")) != filepath.Join(dir, "echo.json") {
		t.Error("sidecar path")
	}

	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	LoadWasmPlugins(reg, dir, reg.Store())
	dyn := reg.Dynamic()
	if len(dyn) != 1 || dyn[0].Name != "echo" || dyn[0].Runtime != "wasm" || dyn[0].Path != filepath.Join(dir, "echo.wasm") {
		t.Fatalf("dynamic = %+v", dyn)
	}
	p := reg.Plugins()[0].(*Plugin)
	if hosts := p.AllowedHosts(); len(hosts) != 1 || hosts[0] != "api.example.test" {
		t.Errorf("allowed hosts from sidecar = %v", hosts)
	}
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := reg.Store().Get("echo", "init"); string(v) != "1" {
		t.Error("on_init should have run through the registry with the real store")
	}
	// Missing directory is not an error.
	LoadWasmPlugins(plugin.NewRegistry(db), filepath.Join(dir, "missing"), reg.Store())
}

func TestSidecar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.wasm")
	if hosts, err := ReadSidecar(p); err != nil || hosts != nil {
		t.Errorf("no sidecar → nil, nil; got %v %v", hosts, err)
	}
	os.WriteFile(SidecarPath(p), []byte(`{"allowed_hosts": ["a", "b"]}`), 0644)
	if hosts, err := ReadSidecar(p); err != nil || len(hosts) != 2 {
		t.Errorf("sidecar = %v %v", hosts, err)
	}
	os.WriteFile(SidecarPath(p), []byte(`{bad`), 0644)
	if _, err := ReadSidecar(p); err == nil {
		t.Error("malformed sidecar should error")
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.wasm")
	os.WriteFile(path, echoBytes(t), 0644)
	info, err := Validate(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "echo" || info.DisplayName != "Echo" || info.Version != "1.2.3" || info.Runtime != "wasm" {
		t.Errorf("info = %+v", info)
	}
	bad := filepath.Join(dir, "bad.wasm")
	os.WriteFile(bad, []byte("nope"), 0644)
	if _, err := Validate(bad); err == nil {
		t.Error("garbage should fail validation")
	}
}
