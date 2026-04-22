// Package cache is a small content-addressable disk cache for module
// source trees downloaded from a Terraform registry or a git repository.
//
// Cache entries are immutable: once a concrete version of a module has
// been populated at a key, its contents do not change. Presence of the
// final directory on disk is authoritative — there is no manifest, no
// metadata, no TTL. A registry's version number or a git commit SHA is
// the identity.
//
// This package intentionally does no network or VCS work itself; it
// exposes Put(), which takes a callback that writes module contents into
// a temporary directory. On success the directory is atomically renamed
// to its final location.
package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind distinguishes how an entry was obtained, which changes its on-disk
// layout so cache entries of different provenance never collide.
type Kind int

const (
	KindRegistry Kind = iota + 1
	KindGit
)

// Key identifies a cache entry.
//
// For KindRegistry: Host is the registry host (e.g. "registry.terraform.io"),
// Path is "namespace/name/provider", Version is a concrete semver, Subdir
// is empty.
//
// For KindGit: Host is the git host (e.g. "github.com"), Path is the
// repo path ("foo/bar.git"), Version is the resolved commit SHA or ref,
// and Subdir is the "//subdir" portion of the source URL (empty for the
// repo root).
type Key struct {
	Kind    Kind
	Host    string
	Path    string
	Version string
	Subdir  string
}

// Cache is a content store rooted at a single directory.
type Cache struct {
	root string
}

// New returns a cache rooted at root. The directory is created lazily on
// the first Put.
func New(root string) *Cache { return &Cache{root: root} }

// Default returns a cache rooted at the user's OS cache directory plus
// "tflens/modules" (e.g. ~/.cache/tflens/modules on Linux,
// %LocalAppData%\tflens\modules on Windows).
func Default() (*Cache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locating user cache dir: %w", err)
	}
	return New(filepath.Join(base, "tflens", "modules")), nil
}

// Root returns the root directory of the cache.
func (c *Cache) Root() string { return c.root }

// Path returns the directory where k's contents live (or would live).
// The path is returned regardless of whether the entry exists.
func (c *Cache) Path(k Key) string {
	subdir := k.Subdir
	if subdir == "" {
		subdir = "_root"
	}
	host := sanitize(k.Host)
	pathSeg := sanitize(k.Path)
	ver := sanitize(k.Version)
	sub := sanitize(subdir)
	switch k.Kind {
	case KindRegistry:
		return filepath.Join(c.root, "registry", host, pathSeg, ver)
	case KindGit:
		return filepath.Join(c.root, "git", host, pathSeg, ver, sub)
	default:
		return filepath.Join(c.root, "unknown", host, pathSeg, ver, sub)
	}
}

// sanitize %-encodes characters that are illegal in Windows path
// components (and are therefore unsafe in any cross-platform cache). It
// preserves '/' so multi-segment keys like "namespace/name/provider" or
// "foo/bar.git" stay laid out as nested directories.
func sanitize(s string) string {
	const illegal = `<>:"\|?*%`
	needs := false
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(illegal, r) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(illegal, r) {
			fmt.Fprintf(&b, "%%%02X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Has reports whether the entry for k is populated on disk.
func (c *Cache) Has(k Key) bool {
	info, err := os.Stat(c.Path(k))
	return err == nil && info.IsDir()
}

// ErrAlreadyPopulated is returned by populate callbacks that wish to
// abort because another process already filled the final path. Callers
// never see it — Put treats it as a cache hit.
var ErrAlreadyPopulated = errors.New("cache: entry already populated")

// Put populates the cache entry for k by invoking populate on a fresh
// temporary directory. On success the temp dir is atomically renamed to
// the final cache location and that location is returned. If k is
// already populated, populate is not invoked and the existing path is
// returned. If populate fails, the temp dir is removed and the error is
// returned unchanged.
func (c *Cache) Put(k Key, populate func(tmpDir string) error) (string, error) {
	final := c.Path(k)
	if c.Has(k) {
		return final, nil
	}

	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("cache: creating parent dir: %w", err)
	}

	tmp, err := os.MkdirTemp(parent, ".tflens-populate-*")
	if err != nil {
		return "", fmt.Errorf("cache: creating temp dir: %w", err)
	}
	if err := populate(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}

	if err := os.Rename(tmp, final); err != nil {
		// Lost a race with a concurrent populate — the destination
		// already exists. Our tmp is now stale; the winner's content
		// is what the caller gets.
		if c.Has(k) {
			_ = os.RemoveAll(tmp)
			return final, nil
		}
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("cache: finalising entry: %w", err)
	}
	return final, nil
}
