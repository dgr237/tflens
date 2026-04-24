package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/loader"
)

// prepareRefCheckout stages a git worktree checked out at baseRef so
// the caller can load it as a second workspace. newDir is the caller's
// "current" workspace — typically cwd on a feature branch. baseRef can
// be any ref git rev-parse understands (branch, tag, SHA, HEAD~3,
// origin/main, …). The returned oldDir is the workspace's equivalent
// path inside the worktree. cleanup MUST be called (usually deferred)
// to remove the worktree.
//
// The workspace's position within the repo is obtained via
// `git rev-parse --show-prefix` rather than filepath.Rel on top-vs-newDir.
// This avoids tripping over Windows 8.3 short names (RUNNER~1 vs
// runneradmin) and macOS /private/var symlinks, both of which cause git
// and Go to disagree about the canonical path form.
func prepareRefCheckout(newDir, baseRef string) (oldDir string, cleanup func(), err error) {
	newAbs, err := filepath.Abs(newDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	top, err := gitTopLevel(newAbs)
	if err != nil {
		return "", nil, fmt.Errorf("workspace is not inside a git repository: %w", err)
	}
	if err := gitRevParse(top, baseRef); err != nil {
		return "", nil, fmt.Errorf("base ref %q not found: %w", baseRef, err)
	}
	prefix, err := gitShowPrefix(newAbs)
	if err != nil {
		return "", nil, fmt.Errorf("locating workspace within repo: %w", err)
	}

	worktreeDir, err := os.MkdirTemp("", "tflens-ref-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp worktree dir: %w", err)
	}
	// git worktree add requires the destination to not exist — MkdirTemp
	// just made it, so remove it first.
	_ = os.Remove(worktreeDir)
	if err := gitWorktreeAdd(top, worktreeDir, baseRef); err != nil {
		return "", nil, err
	}

	oldDir = worktreeDir
	if prefix != "" {
		oldDir = filepath.Join(worktreeDir, filepath.FromSlash(prefix))
	}

	cleanup = func() {
		gitWorktreeRemove(top, worktreeDir)
		_ = os.RemoveAll(worktreeDir)
	}
	return oldDir, cleanup, nil
}

func gitTopLevel(cwd string) (string, error) {
	out, err := runGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitRevParse(top, ref string) error {
	_, err := runGit(top, "rev-parse", "--verify", ref+"^{commit}")
	return err
}

// gitShowPrefix returns the workspace's path relative to the repository
// root, with forward slashes, or "" when the workspace IS the repository
// root. Authoritative for locating the equivalent path inside a sibling
// worktree, avoiding disagreements between Go's filepath and the on-disk
// path form that varies by OS (8.3 short names on Windows, /private/var
// symlinks on macOS).
func gitShowPrefix(cwd string) (string, error) {
	out, err := runGit(cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(out), "/"), nil
}

// RefAutoKeyword is the user-facing keyword that triggers base-ref
// auto-detection, e.g. `tflens diff --ref auto`. Chosen over pflag's
// NoOptDefVal because that would make `--ref main <ws>` parse as
// `--ref=<auto>` plus a positional `main` — worse UX than an explicit
// keyword.
const RefAutoKeyword = "auto"

// resolveAutoRef picks a sensible base ref when the user passed --ref
// auto. Tries (in order): the current branch's upstream, origin/HEAD's
// symbolic target, then bare "main" and "master". Returns the first
// resolvable ref as a human-readable name (e.g. "origin/main"). Returns
// an error if nothing matches so the user knows they need to pass --ref
// <ref> explicitly.
func resolveAutoRef(workspace string) (string, error) {
	if out, err := runGit(workspace, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref, nil
		}
	}
	if out, err := runGit(workspace, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		ref := strings.TrimSpace(out)
		// If origin/HEAD is unset, git either errors or echoes the ref
		// name back. Reject the echo so we fall through.
		if ref != "" && ref != "origin/HEAD" {
			return ref, nil
		}
	}
	for _, ref := range []string{"main", "master"} {
		if err := gitRevParse(workspace, ref); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("could not auto-detect base ref (tried @{upstream}, origin/HEAD, main, master); pass --ref <ref> explicitly")
}

func gitWorktreeAdd(top, dest, ref string) error {
	_, err := runGit(top, "worktree", "add", "--detach", "--quiet", dest, ref)
	if err != nil {
		return fmt.Errorf("git worktree add %s @%s: %w", dest, ref, err)
	}
	return nil
}

func gitWorktreeRemove(top, dest string) {
	// Forced remove: if the worktree is somehow dirty, we still want to
	// drop the registration so we don't leak entries in .git/worktrees.
	_, _ = runGit(top, "worktree", "remove", "--force", dest)
}

func runGit(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// loadOldProjectForRef loads the workspace checked out at baseRef using
// its own resolver chain. It returns the loaded project and a cleanup
// func for the backing git worktree.
func loadOldProjectForRef(cmd *cobra.Command, newWs, baseRef string) (*loader.Project, func(), error) {
	oldDir, cleanup, err := prepareRefCheckout(newWs, baseRef)
	if err != nil {
		return nil, nil, err
	}
	proj, err := loadProject(cmd, oldDir)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("loading workspace at %s: %w", baseRef, err)
	}
	return proj, cleanup, nil
}
