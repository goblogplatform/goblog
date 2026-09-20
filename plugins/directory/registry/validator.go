package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"goblog/plugin"
	"goblog/plugin/wasm"
)

// Validator loads a plugin module the way goblog would and reports its
// identity. The real one instantiates the module in-process; tests use
// FakeValidator.
type Validator interface {
	Validate(ctx context.Context, module []byte) (plugin.Info, error)
}

// WasmValidator instantiates the module with goblog's own runtime — no
// store, no allowed hosts, the runtime's memory cap and call timeouts — and
// reads its identity export. That is the same sandbox an installed plugin
// runs in, so validating a submission is no more exposure than a site
// installing it.
type WasmValidator struct{}

// loadMu serializes instantiation: a burst of public submissions must not
// stack several 64 MB instances at once.
var loadMu sync.Mutex

func (WasmValidator) Validate(ctx context.Context, module []byte) (plugin.Info, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return plugin.Info{}, err
	}
	p, err := wasm.LoadBytes(module, wasm.Options{Logf: func(string, ...any) {}})
	if err != nil {
		return plugin.Info{}, err
	}
	defer p.Close()
	id := p.Identity()
	if id.Name == "" || id.Version == "" {
		return plugin.Info{}, errors.New("identity returned an empty name or version")
	}
	return plugin.Info{Name: id.Name, DisplayName: id.DisplayName, Version: id.Version, Runtime: "wasm"}, nil
}

// Sum is the hex sha256 of b — the index's sha256 field and the key
// FakeValidator answers by.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FakeValidator answers by the sha256 of the module bytes it is given. It is
// exported so the directory package's tests can use it.
type FakeValidator struct {
	Infos map[string]plugin.Info
	Err   error
}

func (f *FakeValidator) Validate(_ context.Context, module []byte) (plugin.Info, error) {
	if f.Err != nil {
		return plugin.Info{}, f.Err
	}
	if info, ok := f.Infos[Sum(module)]; ok {
		return info, nil
	}
	return plugin.Info{}, errors.New("fake: does not load")
}
