package resolver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dgr237/tflens/pkg/cache"
)

// GitConfig configures a GitResolver.
type GitConfig struct {
	// Cache stores cloned repositories. Required.
	Cache *cache.Cache
	// GitExec is the git binary to invoke; defaults to "git" (via PATH).
	GitExec string
}

// GitResolver resolves module sources whose contents live in a git
// repository: the "git::" forced-scheme form, and the bare VCS shorthand
// (github.com/foo/bar, bitbucket.org/foo/bar, ...).
//
// A clone is placed in the cache keyed by (host, repo-path, ref) — the
// same ref is never re-cloned, and multiple //subdir consumers share a
// single clone. The .git directory is removed after checkout to save
// space.
type GitResolver struct {
	cfg GitConfig
}

func NewGitResolver(cfg GitConfig) (*GitResolver, error) {
	if cfg.Cache == nil {
		return nil, errors.New("GitConfig.Cache is required")
	}
	if cfg.GitExec == "" {
		cfg.GitExec = "git"
	}
	return &GitResolver{cfg: cfg}, nil
}

func (r *GitResolver) Resolve(ctx context.Context, ref Ref) (*Resolved, error) {
	gs, ok := parseGitSource(ref.Source)
	if !ok {
		return nil, ErrNotApplicable
	}
	// Honour an explicit `version = "..."` attribute when the source URL
	// has no ref= of its own. This matches Terraform's behaviour: the
	// version attribute is an alternative way to pin the ref.
	if gs.ref == "" && ref.Version != "" {
		gs.ref = ref.Version
	}
	return r.resolve(ctx, gs)
}

// FetchGit is the hook RegistryResolver uses when its download URL turns
// out to be git-backed. rawURL is the URL with its `git::` prefix already
// stripped. The returned directory is the (root of the) module, with any
// //subdir in rawURL already applied; callers layer their own subdir
// concept on top.
func (r *GitResolver) FetchGit(ctx context.Context, rawURL string) (string, error) {
	gs, ok := parseGitSource("git::" + rawURL)
	if !ok {
		return "", fmt.Errorf("cannot parse git URL %q", rawURL)
	}
	res, err := r.resolve(ctx, gs)
	if err != nil {
		return "", err
	}
	return res.Dir, nil
}

func (r *GitResolver) resolve(ctx context.Context, gs gitSource) (*Resolved, error) {
	version := gs.ref
	if version == "" {
		version = "HEAD"
	}
	key := cache.Key{
		Kind:    cache.KindGit,
		Host:    gs.host,
		Path:    gs.path,
		Version: version,
	}
	clonePath := r.cfg.Cache.Path(key)

	if !r.cfg.Cache.Has(key) {
		if _, err := r.cfg.Cache.Put(key, func(tmp string) error {
			return r.clone(ctx, gs.cloneURL, gs.ref, tmp)
		}); err != nil {
			return nil, err
		}
	}

	dir := clonePath
	if gs.subdir != "" {
		dir = filepath.Join(dir, filepath.FromSlash(gs.subdir))
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("subdir %q not found in %s@%s: %w", gs.subdir, gs.cloneURL, version, err)
		}
	}
	return &Resolved{Dir: dir, Version: version, Kind: KindGit}, nil
}

// clone runs `git clone` then (if ref is set) `git checkout <ref>`. The
// .git directory is removed on success — tflens only needs the working
// tree and this keeps the cache size sane for repos with large history.
func (r *GitResolver) clone(ctx context.Context, repoURL, ref, destDir string) error {
	cmd := exec.CommandContext(ctx, r.cfg.GitExec, "clone", "--quiet", repoURL, destDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", repoURL, err, out)
	}
	if ref != "" {
		cmd := exec.CommandContext(ctx, r.cfg.GitExec, "-C", destDir, "checkout", "--quiet", ref)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s: %w\n%s", ref, err, out)
		}
	}
	_ = os.RemoveAll(filepath.Join(destDir, ".git"))
	return nil
}
