package wasm

import (
	"context"
	"encoding/json"

	"goblog/plugin"

	extism "github.com/extism/go-sdk"
)

// hostFunctions builds the store_* host functions (namespace
// extism:host/user). They read the plugin's name through p at call time,
// because the name is only known after the identity export has run.
//
// Shapes: store_get(key) → value ("" when missing), store_set(key, value) → 0/1,
// store_delete(key) → 0/1, store_list(prefix) → JSON array of keys.
func hostFunctions(p *Plugin) []extism.HostFunction {
	i64 := []extism.ValueType{extism.ValueTypeI64}
	fail := func(cp *extism.CurrentPlugin, err error) uint64 {
		cp.Log(extism.LogLevelWarn, "store: "+err.Error())
		off, _ := cp.WriteString("")
		return off
	}
	get := extism.NewHostFunctionWithStack("store_get", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err := cp.ReadString(stack[0])
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		v, _, err := p.opts.Store.Get(p.name, key)
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		stack[0], _ = cp.WriteBytes(v)
	}, i64, i64)
	set := extism.NewHostFunctionWithStack("store_set", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err1 := cp.ReadString(stack[0])
		val, err2 := cp.ReadBytes(stack[1])
		if err1 != nil || err2 != nil {
			stack[0] = 1
			return
		}
		if err := p.opts.Store.Set(p.name, key, val); err != nil {
			cp.Log(extism.LogLevelWarn, "store_set: "+err.Error())
			stack[0] = 1
			return
		}
		stack[0] = 0
	}, []extism.ValueType{extism.ValueTypeI64, extism.ValueTypeI64}, i64)
	del := extism.NewHostFunctionWithStack("store_delete", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		key, err := cp.ReadString(stack[0])
		if err != nil || p.opts.Store.Delete(p.name, key) != nil {
			stack[0] = 1
			return
		}
		stack[0] = 0
	}, i64, i64)
	list := extism.NewHostFunctionWithStack("store_list", func(ctx context.Context, cp *extism.CurrentPlugin, stack []uint64) {
		prefix, err := cp.ReadString(stack[0])
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		keys, err := p.opts.Store.List(p.name, prefix)
		if err != nil {
			stack[0] = fail(cp, err)
			return
		}
		if keys == nil {
			keys = []string{}
		}
		b, _ := json.Marshal(keys)
		stack[0], _ = cp.WriteBytes(b)
	}, i64, i64)
	return []extism.HostFunction{get, set, del, list}
}

// nopStore is used when no Store is configured (validation): every write
// fails, every read is empty.
type nopStore struct{}

func (nopStore) Get(string, string) ([]byte, bool, error) { return nil, false, nil }
func (nopStore) Set(string, string, []byte) error         { return plugin.ErrStoreUnavailable }
func (nopStore) Delete(string, string) error              { return plugin.ErrStoreUnavailable }
func (nopStore) List(string, string) ([]string, error)    { return nil, nil }
func (nopStore) DeleteAll(string) error                   { return plugin.ErrStoreUnavailable }
