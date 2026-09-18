package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gplugin "goblog/plugin"
	"goblog/plugin/wasm"
)

// runValidatePlugin implements `goblog validate-plugin <file.go>`: it loads
// the file through the dynamic plugin loader and prints the plugin's identity
// as JSON. Returns the process exit code. The plugin registry's CI runs this
// against the release Docker image to check a submission before listing it.
func runValidatePlugin(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: goblog validate-plugin <file.go>")
		return 2
	}
	var info gplugin.Info
	var err error
	if strings.HasSuffix(strings.ToLower(args[0]), ".wasm") {
		info, err = wasm.Validate(args[0])
	} else {
		info, err = gplugin.Validate(args[0])
	}
	if err != nil {
		fmt.Fprintf(stderr, "invalid plugin: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(info); err != nil {
		fmt.Fprintf(stderr, "write result: %v\n", err)
		return 1
	}
	return 0
}
