package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchFindsByNameAndDepth(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "a.log"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sub", "b.log"), []byte("22"), 0o644)
	hits, _, _, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 0, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "a.log" {
		t.Fatalf("depth 0 hits = %+v", hits)
	}
	hits, _, _, err = Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("depth 1 hits = %+v", hits)
	}
}

func TestSearchTruncatesAndCancels(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.log", "b.log"} {
		_ = os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644)
	}
	hits, _, truncated, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 0, Limit: 1})
	if err != nil || !truncated || len(hits) != 1 {
		t.Fatalf("truncate: hits=%v truncated=%v err=%v", hits, truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err = Search(ctx, root, "", SearchOpts{MaxDepth: 0, Limit: 10})
	if err == nil {
		t.Fatal("expected ctx error")
	}
}

func TestSearchSkipsSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "outside.log"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlink not supported")
	}
	hits, _, _, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 5, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Path == "link/outside.log" {
			t.Fatalf("must not follow symlinked dirs: %+v", hits)
		}
	}
}
