package plugin_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"goblog/plugin"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStore_RoundTrip(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	s := reg.Store()

	if _, found, err := s.Get("a", "missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v", found, err)
	}
	if err := s.Set("a", "k1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", "k2", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b", "k1", []byte("other")); err != nil {
		t.Fatal(err)
	}
	v, found, err := s.Get("a", "k1")
	if err != nil || !found || !bytes.Equal(v, []byte("v1")) {
		t.Errorf("get a/k1 = %q %v %v", v, found, err)
	}
	if err := s.Set("a", "k1", []byte("v1b")); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := s.Get("a", "k1"); string(v) != "v1b" {
		t.Errorf("overwrite failed: %q", v)
	}
	keys, err := s.List("a", "k")
	if err != nil || len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Errorf("list = %v %v", keys, err)
	}
	if keys, _ := s.List("a", "k2"); len(keys) != 1 {
		t.Errorf("prefix list = %v", keys)
	}
	if err := s.Delete("a", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get("a", "k1"); found {
		t.Error("k1 should be deleted")
	}
	if v, _, _ := s.Get("b", "k1"); string(v) != "other" {
		t.Error("plugins must be isolated")
	}
	if err := s.DeleteAll("a"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.List("a", ""); len(keys) != 0 {
		t.Errorf("DeleteAll left %v", keys)
	}
	if v, _, _ := s.Get("b", "k1"); string(v) != "other" {
		t.Error("DeleteAll must not touch other plugins")
	}
}

func TestStore_Limits(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	reg := plugin.NewRegistry(db)
	reg.Init()
	s := reg.Store()
	if err := s.Set("a", strings.Repeat("k", plugin.MaxStoreKeyBytes+1), []byte("v")); err == nil {
		t.Error("oversized key should be rejected")
	}
	if err := s.Set("a", "k", make([]byte, plugin.MaxStoreValueBytes+1)); err == nil {
		t.Error("oversized value should be rejected")
	}
	if err := s.Set("a", "", []byte("v")); err == nil {
		t.Error("empty key should be rejected")
	}
	if err := s.Set("a", "k", make([]byte, plugin.MaxStoreValueBytes)); err != nil {
		t.Errorf("max-size value should be accepted: %v", err)
	}
}

func TestStore_NoDB(t *testing.T) {
	reg := plugin.NewRegistry(nil)
	s := reg.Store()
	if _, _, err := s.Get("a", "k"); !errors.Is(err, plugin.ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable, got %v", err)
	}
	if err := s.Set("a", "k", []byte("v")); !errors.Is(err, plugin.ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable, got %v", err)
	}
}

func newTestStore(t *testing.T) plugin.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	reg := plugin.NewRegistry(db)
	if err := reg.Init(); err != nil {
		t.Fatal(err)
	}
	return reg.Store()
}

// TestStore_PluginQuota: a plugin's total stored bytes and row count are
// capped; other plugins' data does not count against it.
func TestStore_PluginQuota(t *testing.T) {
	s := newTestStore(t)
	big := make([]byte, plugin.MaxStoreValueBytes)
	n := plugin.MaxStorePluginBytes / plugin.MaxStoreValueBytes
	for i := 0; i < n; i++ {
		if err := s.Set("a", fmt.Sprintf("k%d", i), big); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if err := s.Set("a", "one-more", []byte("x")); err == nil {
		t.Error("expected the byte quota to reject one more value")
	}
	// Overwriting an existing key does not count its old value twice.
	if err := s.Set("a", "k0", big); err != nil {
		t.Errorf("overwrite within quota: %v", err)
	}
	if err := s.Set("b", "k", big); err != nil {
		t.Errorf("another plugin is unaffected: %v", err)
	}
	if err := s.Delete("a", "k0"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("a", "one-more", []byte("x")); err != nil {
		t.Errorf("after freeing space: %v", err)
	}
}

func TestStore_PluginRowQuota(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < plugin.MaxStorePluginRows; i++ {
		if err := s.Set("a", fmt.Sprintf("k%d", i), []byte("v")); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if err := s.Set("a", "extra", []byte("v")); err == nil {
		t.Error("expected the row quota to reject a new key")
	}
	if err := s.Set("a", "k0", []byte("w")); err != nil {
		t.Errorf("overwriting an existing key is allowed at the row cap: %v", err)
	}
}
