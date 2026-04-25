package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheStatsCountsEntries(t *testing.T) {
	root := t.TempDir()
	mkfile := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("registry/host/ns/name/prov/1.0.0/main.tf", "# one")
	mkfile("registry/host/ns/name/prov/1.0.0/vars.tf", "# same entry")
	mkfile("registry/host/ns/name/prov/2.0.0/main.tf", "# two")
	mkfile("git/github.com/foo/bar.git/v1/main.tf", "# three")

	entries, bytes, err := cacheStats(root)
	if err != nil {
		t.Fatalf("cacheStats: %v", err)
	}
	// 3 leaf directories that contain regular files directly:
	//   registry/host/ns/name/prov/1.0.0
	//   registry/host/ns/name/prov/2.0.0
	//   git/github.com/foo/bar.git/v1
	if entries != 3 {
		t.Errorf("entries = %d, want 3", entries)
	}
	if bytes == 0 {
		t.Errorf("bytes = 0, want nonzero")
	}
}

func TestCacheStatsMissingRootIsZero(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	entries, bytes, err := cacheStats(root)
	if err != nil {
		t.Fatalf("cacheStats on missing root: %v", err)
	}
	if entries != 0 || bytes != 0 {
		t.Errorf("missing root: got entries=%d bytes=%d, want 0 0", entries, bytes)
	}
}
