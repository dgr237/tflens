package loader_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

// TestPrepareWorktreeAtBaseRefMaterializesOldFiles: the worktree
// returned by PrepareWorktree contains the file content from baseRef,
// not from the current working tree.
func TestPrepareWorktreeAtBaseRefMaterializesOldFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := newRepoWithTwoCommits(t)
	workspace := filepath.Join(repo, "workspace")

	oldDir, cleanup, err := loader.PrepareWorktree(workspace, "main")
	if err != nil {
		t.Fatalf("PrepareWorktree: %v", err)
	}
	defer cleanup()

	got, err := os.ReadFile(filepath.Join(oldDir, "main.tf"))
	if err != nil {
		t.Fatalf("read worktree main.tf: %v", err)
	}
	// strings.Contains avoids a Windows CRLF mismatch — git on
	// Windows runners can apply autocrlf to checked-out content.
	if !strings.Contains(string(got), "version = 1") {
		t.Errorf("worktree shows %q; want main-branch content (version = 1)", string(got))
	}
}

func TestPrepareWorktreeOutsideRepoIsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, _, err := loader.PrepareWorktree(t.TempDir(), "main"); err == nil {
		t.Error("expected error for non-repo workspace")
	}
}

func TestPrepareWorktreeUnknownRefIsError(t *testing.T) {
	repo := newRepoWithTwoCommits(t)
	if _, _, err := loader.PrepareWorktree(filepath.Join(repo, "workspace"), "no-such-ref"); err == nil {
		t.Error("expected error for unknown ref")
	}
}

func TestResolveAutoRefPrefersExistingBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := newRepoWithTwoCommits(t)
	got, err := loader.ResolveAutoRef(repo)
	if err != nil {
		t.Fatalf("ResolveAutoRef: %v", err)
	}
	// The repo helper uses init -b main and never sets an upstream
	// or origin remote, so the fallback path should pick "main".
	if got != "main" {
		t.Errorf("ResolveAutoRef = %q, want main", got)
	}
}

func TestResolveAutoRefNothingMatchesIsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "-b", "feature")
	// Empty repo with no main/master and no upstream — auto should fail.
	if _, err := loader.ResolveAutoRef(dir); err == nil {
		t.Error("expected error when no candidate ref exists")
	}
}

// newRepoWithTwoCommits creates a repo with `workspace/main.tf` at
// "version = 1" on `main`, then a `feature` branch where the file
// reads "version = 2". Returns the repo's absolute path.
func newRepoWithTwoCommits(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "-b", "main")

	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.tf"), []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "v1")

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(workspace, "main.tf"), []byte("version = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "v2")
	return dir
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
