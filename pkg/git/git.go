// Package git wraps the small set of git plumbing commands tflens
// needs to manage temporary worktrees for ref-comparison subcommands
// (diff, whatif, statediff). It is deliberately minimal — no
// porcelain, no caching, no authentication — and has no dependency
// on any other tflens package.
//
// All functions shell out to the `git` binary on $PATH. cwd
// arguments must be absolute or already-CWD-relative paths the
// shelled-out process can stat.
package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// TopLevel returns the absolute path of the repository root that
// contains cwd. Wraps `git rev-parse --show-toplevel`.
func TopLevel(cwd string) (string, error) {
	out, err := Run(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RevParse verifies that ref resolves to a commit inside the
// repository at top. Wraps `git rev-parse --verify <ref>^{commit}`.
// Returns nil on success and a wrapped error when the ref doesn't
// exist or doesn't resolve to a commit.
func RevParse(top, ref string) error {
	_, err := Run(top, "rev-parse", "--verify", ref+"^{commit}")
	return err
}

// ShowPrefix returns the path of cwd relative to the repository
// root, with forward slashes and no trailing slash. Returns "" when
// cwd IS the repository root.
//
// Authoritative for locating the equivalent path inside a sibling
// worktree, avoiding disagreements between Go's filepath and the
// on-disk path form (Windows 8.3 short names, macOS /private/var
// symlinks).
func ShowPrefix(cwd string) (string, error) {
	out, err := Run(cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(out), "/"), nil
}

// WorktreeAdd creates a detached worktree at dest checked out at ref.
// dest MUST not exist when called; git creates it. Wraps
// `git worktree add --detach --quiet <dest> <ref>`.
func WorktreeAdd(top, dest, ref string) error {
	_, err := Run(top, "worktree", "add", "--detach", "--quiet", dest, ref)
	if err != nil {
		return fmt.Errorf("git worktree add %s @%s: %w", dest, ref, err)
	}
	return nil
}

// WorktreeRemove drops the worktree at dest from the repository's
// `.git/worktrees` registry. Always uses --force so a dirty
// worktree (e.g. one whose loader left .terraform/ files behind)
// still gets cleaned up. Errors are silently swallowed because the
// caller almost always also `os.RemoveAll`s the directory itself.
func WorktreeRemove(top, dest string) {
	_, _ = Run(top, "worktree", "remove", "--force", dest)
}

// Run executes `git <args...>` with the working directory set to
// cwd and returns the combined stdout + stderr. On a non-zero exit
// the returned error wraps the command + output for easy diagnosis.
func Run(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
