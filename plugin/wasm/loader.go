package wasm

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"goblog/plugin"
)

// sidecar is plugins/wasm/<name>.json next to <name>.wasm.
type sidecar struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

// SidecarPath returns the sidecar file for a .wasm path.
func SidecarPath(wasmPath string) string {
	return strings.TrimSuffix(wasmPath, filepath.Ext(wasmPath)) + ".json"
}

// ReadSidecar returns the allowed hosts declared next to a plugin file;
// nil when there is no sidecar.
func ReadSidecar(wasmPath string) ([]string, error) {
	b, err := os.ReadFile(SidecarPath(wasmPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s sidecar
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", SidecarPath(wasmPath), err)
	}
	return s.AllowedHosts, nil
}

// WriteSidecar records the allowed hosts for a plugin file.
func WriteSidecar(wasmPath string, allowedHosts []string) error {
	if allowedHosts == nil {
		allowedHosts = []string{}
	}
	b, _ := json.MarshalIndent(sidecar{AllowedHosts: allowedHosts}, "", "  ")
	return os.WriteFile(SidecarPath(wasmPath), append(b, '\n'), 0644)
}

// LoadWasmPlugins loads every *.wasm in dir and registers it. A file that
// fails to load is logged and skipped; a missing dir is not an error.
func LoadWasmPlugins(registry *plugin.Registry, dir string, store plugin.Store) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read wasm plugin directory %s: %v", dir, err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wasm") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		hosts, err := ReadSidecar(path)
		if err != nil {
			log.Printf("Warning: skipping %s: %v", e.Name(), err)
			continue
		}
		p, err := Load(path, Options{Store: store, AllowedHosts: hosts})
		if err != nil {
			log.Printf("Warning: failed to load wasm plugin %s: %v", e.Name(), err)
			continue
		}
		if err := registry.RegisterDynamic(p, path); err != nil {
			log.Printf("Warning: skipping wasm plugin %s: %v", e.Name(), err)
			p.Close()
		}
	}
}

// Validate loads a .wasm file with no store or network and reports its
// identity — what `goblog validate-plugin` prints for wasm files.
func Validate(path string) (plugin.Info, error) {
	p, err := Load(path, Options{})
	if err != nil {
		return plugin.Info{}, err
	}
	defer p.Close()
	id := p.Identity()
	return plugin.Info{Name: id.Name, DisplayName: id.DisplayName, Version: id.Version, Runtime: "wasm"}, nil
}
