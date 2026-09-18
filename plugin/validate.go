package plugin

import "fmt"

// Info is what `goblog validate-plugin` reports about a dynamic plugin file.
type Info struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
	Runtime     string `json:"runtime,omitempty"`
}

// Validate loads a single dynamic plugin file through the Yaegi loader,
// exactly as LoadDynamicPlugins does at startup, and reports its identity.
// The plugin registry's CI runs it (via `goblog validate-plugin`) to check a
// submitted plugin loads and that its Name and Version match the manifest
// and release tag.
func Validate(path string) (Info, error) {
	p, err := LoadDynamicPlugin(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{Name: p.Name(), DisplayName: p.DisplayName(), Version: p.Version()}
	if info.Name == "" {
		return Info{}, fmt.Errorf("%s: Name() returned an empty string", path)
	}
	if info.Version == "" {
		return Info{}, fmt.Errorf("%s: Version() returned an empty string", path)
	}
	return info, nil
}
