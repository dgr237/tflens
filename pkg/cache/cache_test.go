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

func TestPathGitLayoutIncludesSubdir(t *testing.T) {
	c := cache.New("/root")
	got := c.Path(gitKey())
	want := filepath.Clean("/root/git/github.com/foo/bar.git/v1.2.3/modules/vpc")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestPathGitRepoRootUsesPlaceholder(t *testing.T) {
	c := cache.New("/root")
	k := gitKey()
	k.Subdir = ""
	got := c.Path(k)
	if !strings.HasSuffix(filepath.ToSlash(got), "/v1.2.3/_root") {
		t.Errorf("Path = %q, want suffix .../v1.2.3/_root", got)
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
