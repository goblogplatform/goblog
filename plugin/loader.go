package plugin

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// LoadDynamicPlugins scans a directory for .go plugin files and loads them
// using the Yaegi Go interpreter. Each file must use `package main` and
// define a function:
//
//	func NewPlugin() plugin.Plugin
//
// The returned plugins are registered with the given registry.
func LoadDynamicPlugins(registry *Registry, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read plugin directory %s: %v", dir, err)
		}
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		p, err := LoadDynamicPlugin(path)
		if err != nil {
			log.Printf("Warning: failed to load plugin %s: %v", entry.Name(), err)
			continue
		}
		if err := registry.RegisterDynamic(p, path); err != nil {
			log.Printf("Warning: skipping plugin %s: %v", entry.Name(), err)
		}
	}
}

// LoadDynamicPlugin loads one plugin source file through the interpreter.
func LoadDynamicPlugin(path string) (Plugin, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := LoadDynamicPluginBytes(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// LoadDynamicPluginBytes interprets plugin source and calls its NewPlugin().
// The source runs as Go code inside this process; only load what you trust.
func LoadDynamicPluginBytes(src []byte) (Plugin, error) {
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		return nil, err
	}
	if err := i.Use(Symbols); err != nil {
		return nil, err
	}
	if _, err := i.Eval(string(src)); err != nil {
		return nil, err
	}
	v, err := i.Eval("NewPlugin()")
	if err != nil {
		return nil, err
	}
	p, ok := v.Interface().(Plugin)
	if !ok {
		return nil, fmt.Errorf("NewPlugin() did not return a plugin.Plugin")
	}
	return p, nil
}
