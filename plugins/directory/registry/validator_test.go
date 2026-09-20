package registry

import (
	"context"
	"errors"
	"os"
	"testing"

	"goblog/plugin"
)

func echoModule(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../plugin/wasm/testdata/echo.wasm")
	if err != nil {
		t.Fatalf("fixture missing (run go generate ./plugin/wasm): %v", err)
	}
	return b
}

func TestWasmValidator_ReportsIdentity(t *testing.T) {
	info, err := WasmValidator{}.Validate(context.Background(), echoModule(t))
	if err != nil {
		t.Fatal(err)
	}
	want := plugin.Info{Name: "echo", DisplayName: "Echo", Version: "1.2.3", Runtime: "wasm"}
	if info != want {
		t.Errorf("info = %+v, want %+v", info, want)
	}
}

func TestWasmValidator_RejectsGarbage(t *testing.T) {
	if _, err := (WasmValidator{}).Validate(context.Background(), []byte("not wasm")); err == nil {
		t.Error("garbage bytes must not validate")
	}
}

func TestWasmValidator_HonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (WasmValidator{}).Validate(ctx, echoModule(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestFakeValidator(t *testing.T) {
	m := []byte("\x00asm x")
	f := &FakeValidator{Infos: map[string]plugin.Info{Sum(m): {Name: "x", Version: "1.0.0", Runtime: "wasm"}}}
	if info, err := f.Validate(context.Background(), m); err != nil || info.Name != "x" {
		t.Errorf("known module: %+v %v", info, err)
	}
	if _, err := f.Validate(context.Background(), []byte("other")); err == nil {
		t.Error("unknown module must fail")
	}
	f.Err = errors.New("boom")
	if _, err := f.Validate(context.Background(), m); err == nil {
		t.Error("Err must be returned")
	}
}
