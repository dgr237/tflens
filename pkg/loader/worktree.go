package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgr237/tflens/pkg/git"
	"github.com/dgr237/tflens/pkg/resolver"
)

// prepareWorktree stages a git worktree checked out at baseRef so the
// caller can load it as a second workspace alongside the working
// tree. workspace is the caller's "current" workspace — typically
// cwd on a feature branch. baseRef can be any ref `git rev-parse`
// understands (branch, tag, SHA, HEAD~3, origin/main, …).
//
// Returns oldDir (the workspace's equivalent path inside the
// worktree) and cleanup (which the caller MUST defer to remove the
// worktree).
//
// The workspace's position within the repo is obtained via
// `git rev-parse --show-prefix` rather than filepath.Rel on
// top-vs-workspace — that avoids tripping over Windows 8.3 short
// names (RUNNER~1 vs runneradmin) and macOS /private/var symlinks,
// both of which cause git and Go to disagree about the canonical
// path form.
//
// Internal — called by loadProjectAtRef. External callers use
// Loader.ProjectsForDiff which composes this with the project load.
func prepareWorktree(workspace, baseRef string) (oldDir string, cleanup func(), err error) {
	wsAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	top, err := git.TopLevel(wsAbs)
	if err != nil {
		return "", nil, fmt.Errorf("workspace is not inside a git repository: %w", err)
	}
	if err := git.RevParse(top, baseRef); err != nil {
		return "", nil, fmt.Errorf("base ref %q not found: %w", baseRef, err)
	}
	prefix, err := git.ShowPrefix(wsAbs)
	if err != nil {
		return "", nil, fmt.Errorf("locating workspace within repo: %w", err)
	}

	worktreeDir, err := os.MkdirTemp("", "tflens-ref-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp worktree dir: %w", err)
	}
	// `git worktree add` requires the destination to not exist —
	// MkdirTemp just made it, so remove it first.
	_ = os.Remove(worktreeDir)
	if err := git.WorktreeAdd(top, worktreeDir, baseRef); err != nil {
		return "", nil, err
	}

	oldDir = worktreeDir
	if prefix != "" {
		oldDir = filepath.Join(worktreeDir, filepath.FromSlash(prefix))
	}

	cleanup = func() {
		git.WorktreeRemove(top, worktreeDir)
		_ = os.RemoveAll(worktreeDir)
	}
	return oldDir, cleanup, nil
}

// ResolveAutoRef picks a sensible base ref for "auto"-style flags.
// Tries (in order): the current branch's upstream, origin/HEAD's
// symbolic target, then bare "main" and "master". Returns the first
// resolvable ref as a human-readable name (e.g. "origin/main").
// Returns an error if nothing matches so the caller can prompt the
// user to pass a ref explicitly.
func ResolveAutoRef(workspace string) (string, error) {
	if out, err := git.Run(workspace, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref, nil
		}
	}
	if out, err := git.Run(workspace, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		ref := strings.TrimSpace(out)
		// If origin/HEAD is unset, git either errors or echoes the
		// ref name back. Reject the echo so we fall through.
		if ref != "" && ref != "origin/HEAD" {
			return ref, nil
		}
	}
	for _, ref := range []string{"main", "master"} {
		if err := git.RevParse(workspace, ref); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("could not auto-detect base ref (tried @{upstream}, origin/HEAD, main, master); pass an explicit ref")
}

// loadProjectAtRef stages a worktree at baseRef and loads the project
// inside it using the supplied resolver. Internal — used by
// Loader.ProjectsForDiff to load the "old" side of a comparison.
//
// Returns the loaded project and the cleanup func. Cleanup MUST be
// deferred to remove the temporary worktree.
func loadProjectAtRef(workspace, baseRef string, r resolver.Resolver) (*Project, func(), error) {
	oldDir, cleanup, err := prepareWorktree(workspace, baseRef)
	if err != nil {
		return nil, nil, err
	}
	proj, _, err := loadProjectWith(oldDir, r, nil)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("loading workspace at %s: %w", baseRef, err)
	}
	return proj, cleanup, nil
}
