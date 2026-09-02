package main

import (
	"os"
	"path/filepath"
	"testing"

	"sshore/internal/forward"
)

func TestListLocal(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0755)
	items, err := a.ListLocal(dir)
	if err != nil {
		t.Fatalf("ListLocal: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items got %d: %+v", len(items), items)
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
	}
	if !names["a.txt"] || !names["sub"] {
		t.Fatalf("missing entries: %+v", names)
	}
}

func TestListLocalMissingDirErrors(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	if _, err := a.ListLocal(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}
