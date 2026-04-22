package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// prepareBranchCheckout stages a git worktree checked out at baseRef so
// the caller can load it as a second workspace. newDir is the caller's
// "current" workspace — typically the cwd on a feature branch. The
// returned oldDir is the workspace's equivalent path inside the
// worktree. cleanup MUST be called (usually deferred) to remove the
// worktree.
//
// The workspace's position within the repo is obtained via
// `git rev-parse --show-prefix` rather than filepath.Rel on top-vs-newDir.
// This avoids tripping over Windows 8.3 short names (RUNNER~1 vs
// runneradmin) and macOS /private/var symlinks, both of which cause git
// and Go to disagree about the canonical path form.
func prepareBranchCheckout(newDir, baseRef string) (oldDir string, cleanup func(), err error) {
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

	worktreeDir, err := os.MkdirTemp("", "tflens-branch-*")
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
// root, with forward slashes and a trailing slash, or "" when the
// workspace IS the repository root. Authoritative for locating the
// equivalent path inside a sibling worktree, avoiding disagreements
// between Go's filepath and the on-disk path form that varies by OS
// (8.3 short names on Windows, /private/var symlinks on macOS).
func gitShowPrefix(cwd string) (string, error) {
	out, err := runGit(cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(out), "/"), nil
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

// ---- call pairing -----------------------------------------------------

// moduleCallStatus classifies a module call by its presence across the
// two workspaces.
type moduleCallStatus int

const (
	statusChanged  moduleCallStatus = iota // present in both; may or may not have contentual changes
	statusAdded                            // present in new, absent in old
	statusRemoved                          // present in old, absent in new
)

// modulePair holds a paired module call from old and new workspaces.
// For statusAdded, oldNode is nil. For statusRemoved, newNode is nil.
type modulePair struct {
	name                   string
	status                 moduleCallStatus
	oldSource, newSource   string
	oldVersion, newVersion string
	oldNode, newNode       *loader.ModuleNode
}

// pairModuleCalls joins the root-level module calls of two projects by
// name, producing one modulePair per unique call name.
func pairModuleCalls(oldProj, newProj *loader.Project) []modulePair {
	oldCalls := rootModuleCalls(oldProj)
	newCalls := rootModuleCalls(newProj)

	names := map[string]struct{}{}
	for n := range oldCalls {
		names[n] = struct{}{}
	}
	for n := range newCalls {
		names[n] = struct{}{}
	}

	var out []modulePair
	for name := range names {
		oldC, hasOld := oldCalls[name]
		newC, hasNew := newCalls[name]
		p := modulePair{name: name}
		switch {
		case !hasOld:
			p.status = statusAdded
			p.newSource = newC.source
			p.newVersion = newC.version
			p.newNode = newProj.Root.Children[name]
		case !hasNew:
			p.status = statusRemoved
			p.oldSource = oldC.source
			p.oldVersion = oldC.version
			p.oldNode = oldProj.Root.Children[name]
		default:
			p.status = statusChanged
			p.oldSource = oldC.source
			p.oldVersion = oldC.version
			p.newSource = newC.source
			p.newVersion = newC.version
			p.oldNode = oldProj.Root.Children[name]
			p.newNode = newProj.Root.Children[name]
		}
		out = append(out, p)
	}
	return out
}

type callInfo struct {
	source  string
	version string
}

func rootModuleCalls(p *loader.Project) map[string]callInfo {
	out := map[string]callInfo{}
	if p == nil || p.Root == nil || p.Root.Module == nil {
		return out
	}
	for _, e := range p.Root.Module.Filter(analysis.KindModule) {
		out[e.Name] = callInfo{
			source:  p.Root.Module.ModuleSource(e.Name),
			version: p.Root.Module.ModuleVersion(e.Name),
		}
	}
	return out
}

// loadOldProjectForBranch loads the workspace checked out at baseRef
// using its own resolver chain. It returns the loaded project and a
// cleanup func for the backing git worktree.
func loadOldProjectForBranch(cmd *cobra.Command, newWs, baseRef string) (*loader.Project, func(), error) {
	oldDir, cleanup, err := prepareBranchCheckout(newWs, baseRef)
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
