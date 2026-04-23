package cache_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/cache"
)

func registryKey() cache.Key {
	return cache.Key{
		Kind:    cache.KindRegistry,
		Host:    "registry.terraform.io",
		Path:    "terraform-aws-modules/vpc/aws",
		Version: "5.0.0",
	}
}

func gitKey() cache.Key {
	return cache.Key{
		Kind:    cache.KindGit,
		Host:    "github.com",
		Path:    "foo/bar.git",
		Version: "v1.2.3",
		Subdir:  "modules/vpc",
	}
}

func TestPathRegistryLayout(t *testing.T) {
	c := cache.New("/root")
	got := c.Path(registryKey())
	want := filepath.Clean("/root/registry/registry.terraform.io/terraform-aws-modules/vpc/aws/5.0.0")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestPathGitLayoutStopsAtVersion(t *testing.T) {
	// Path for git ends at {version} regardless of Subdir — one clone of
	// a repo+ref serves all //subdir consumers.
	c := cache.New("/root")
	got := c.Path(gitKey())
	want := filepath.Clean("/root/git/github.com/foo/bar.git/v1.2.3")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}

	// Changing Subdir does NOT change Path — they share the cache entry.
	k2 := gitKey()
	k2.Subdir = "different/subdir"
	if c.Path(k2) != got {
		t.Errorf("different subdirs produced different paths: %q vs %q", got, c.Path(k2))
	}
}

func TestHasReturnsFalseForMissing(t *testing.T) {
	c := cache.New(t.TempDir())
	if c.Has(registryKey()) {
		t.Error("fresh cache should report Has = false")
	}
}

func TestPutThenHasHit(t *testing.T) {
	c := cache.New(t.TempDir())
	k := registryKey()
	dir, err := c.Put(k, func(tmp string) error {
		return os.WriteFile(filepath.Join(tmp, "main.tf"), []byte("# test"), 0o644)
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if dir != c.Path(k) {
		t.Errorf("Put returned %q, want %q", dir, c.Path(k))
	}
	if !c.Has(k) {
		t.Error("Has should return true after Put")
	}
	body, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil || string(body) != "# test" {
		t.Errorf("populated file not readable, err=%v body=%q", err, body)
	}
}

func TestPutSkipsPopulateOnHit(t *testing.T) {
	c := cache.New(t.TempDir())
	k := registryKey()
	if _, err := c.Put(k, func(tmp string) error { return nil }); err != nil {
		t.Fatalf("initial Put: %v", err)
	}
	called := 0
	dir, err := c.Put(k, func(tmp string) error {
		called++
		return nil
	})
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if called != 0 {
		t.Errorf("populate should not be called on hit, got %d calls", called)
	}
	if dir != c.Path(k) {
		t.Errorf("Put returned %q, want %q", dir, c.Path(k))
	}
}

func TestPutFailureLeavesCacheEmpty(t *testing.T) {
	root := t.TempDir()
	c := cache.New(root)
	k := registryKey()
	boom := errors.New("download failed")
	_, err := c.Put(k, func(tmp string) error {
		// populate a partial file, then fail
		_ = os.WriteFile(filepath.Join(tmp, "partial.tf"), []byte("oops"), 0o644)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("Put error = %v, want %v", err, boom)
	}
	if c.Has(k) {
		t.Error("failed Put must not leave an entry in the cache")
	}
	// The temp dir must be cleaned up — no .tflens-populate-* siblings.
	parent := filepath.Dir(c.Path(k))
	entries, err := os.ReadDir(parent)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading parent: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tflens-populate-") {
			t.Errorf("leftover temp dir after failed Put: %s", e.Name())
		}
	}
}

func TestPutCreatesParentDirectories(t *testing.T) {
	// A fresh cache root with no pre-existing subtree still works.
	root := filepath.Join(t.TempDir(), "not", "yet", "there")
	c := cache.New(root)
	_, err := c.Put(registryKey(), func(tmp string) error { return nil })
	if err != nil {
		t.Fatalf("Put on fresh cache: %v", err)
	}
}

func TestDefaultRootUnderUserCacheDir(t *testing.T) {
	c, err := cache.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if !strings.Contains(c.Root(), "tflens") {
		t.Errorf("Default root = %q, expected to contain 'tflens'", c.Root())
	}
}

// Path() routes unknown Kind values into an "unknown/…" subtree so a key
// with Kind 0 (the zero value) doesn't collide with registry/git paths.
func TestPathUnknownKindUsesUnknownSubtree(t *testing.T) {
	c := cache.New("/root")
	got := c.Path(cache.Key{Host: "h", Path: "p", Version: "v"})
	want := filepath.Clean("/root/unknown/h/p/v/_root")
	if got != want {
		t.Errorf("Path(unknown kind) = %q, want %q", got, want)
	}
	// Subdir on an unknown-kind key participates in the path so different
	// subdirs don't collide.
	got2 := c.Path(cache.Key{Host: "h", Path: "p", Version: "v", Subdir: "modules/x"})
	if got == got2 {
		t.Errorf("Subdir should differentiate unknown-kind paths: %q == %q", got, got2)
	}
}

// sanitize must escape characters that are illegal in Windows path
// components — most importantly ':' and '*'. The escape preserves '/' so
// multi-segment keys stay nested directories.
func TestPathSanitisesIllegalCharacters(t *testing.T) {
	c := cache.New("/root")
	k := cache.Key{
		Kind:    cache.KindRegistry,
		Host:    "host:8080", // ':' is illegal on Windows
		Path:    "ns/name/aws",
		Version: "v*1.0", // '*' is illegal on Windows
	}
	p := c.Path(k)
	if strings.Contains(p, ":8080") {
		t.Errorf("Path should escape ':' in host: %q", p)
	}
	if strings.Contains(p, "v*1.0") {
		t.Errorf("Path should escape '*' in version: %q", p)
	}
	// Path-segment '/' must be preserved as a directory separator.
	if !strings.Contains(p, "ns/name/aws") && !strings.Contains(p, `ns\name\aws`) {
		t.Errorf("Path should keep '/' as a separator: %q", p)
	}
}

// Sanitize falls back to a SHA-256 prefix when the encoded segment would
// blow past the 64-character cap — this keeps file:// cache keys with
// long absolute paths from busting Windows MAX_PATH.
func TestPathLongSegmentTruncatedToHash(t *testing.T) {
	long := strings.Repeat("longpathfragment-", 10) // ~170 chars
	c := cache.New("/root")
	p := c.Path(cache.Key{
		Kind:    cache.KindRegistry,
		Host:    "host",
		Path:    long,
		Version: "1.0.0",
	})
	if strings.Contains(p, long) {
		t.Errorf("Path should hash the long segment, not include it verbatim: %q", p)
	}
	// Same input must produce the same hashed segment (stable).
	if c.Path(cache.Key{Kind: cache.KindRegistry, Host: "host", Path: long, Version: "1.0.0"}) != p {
		t.Error("hashed segment is not stable across calls")
	}
}

// When a concurrent writer populates the final cache path between our
// Has() check and our Rename, Put returns the winner's path without an
// error and cleans up our temp dir.
func TestPutRaceLossReturnsWinnerPath(t *testing.T) {
	c := cache.New(t.TempDir())
	k := registryKey()

	dir, err := c.Put(k, func(tmp string) error {
		// Simulate a concurrent winner: create the final destination
		// (and put their content in it) while we are still in our temp
		// directory. The subsequent Rename will fail, and Put should
		// detect the race and return the winner's directory.
		final := c.Path(k)
		if err := os.MkdirAll(final, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(final, "winner.tf"), []byte("winner"), 0o644)
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if dir != c.Path(k) {
		t.Errorf("Put returned %q, want winner path %q", dir, c.Path(k))
	}
	body, err := os.ReadFile(filepath.Join(dir, "winner.tf"))
	if err != nil || string(body) != "winner" {
		t.Errorf("winner content not preserved, err=%v body=%q", err, body)
	}
	// Our temp dir must have been cleaned up.
	parent := filepath.Dir(c.Path(k))
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tflens-populate-") {
			t.Errorf("leftover temp dir after race loss: %s", e.Name())
		}
	}
}
