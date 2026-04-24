package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/git"
)

// repoSetup creates a fresh git repository in a temp dir with one
// initial commit on `main` and returns the repo's absolute path.
// Skips the test if `git` isn't on PATH.
func repoSetup(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "initial")
	return dir
}

func TestTopLevelReturnsRepoRoot(t *testing.T) {
	repo := repoSetup(t)
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := git.TopLevel(sub)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	gotAbs, _ := filepath.EvalSymlinks(got)
	repoAbs, _ := filepath.EvalSymlinks(repo)
	if gotAbs != repoAbs {
		t.Errorf("TopLevel from subdir = %q, want %q", gotAbs, repoAbs)
	}
}

func TestTopLevelOutsideRepoIsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := git.TopLevel(t.TempDir()); err == nil {
		t.Error("expected error for non-repo dir")
	}
}

func TestRevParseValidRef(t *testing.T) {
	repo := repoSetup(t)
	if err := git.RevParse(repo, "main"); err != nil {
		t.Errorf("RevParse(main): %v", err)
	}
}

func TestRevParseUnknownRefIsError(t *testing.T) {
	repo := repoSetup(t)
	if err := git.RevParse(repo, "definitely-not-a-real-ref"); err == nil {
		t.Error("expected error for unknown ref")
	}
}

func TestShowPrefixAtRoot(t *testing.T) {
	repo := repoSetup(t)
	got, err := git.ShowPrefix(repo)
	if err != nil {
		t.Fatalf("ShowPrefix: %v", err)
	}
	if got != "" {
		t.Errorf("ShowPrefix at repo root = %q, want empty", got)
	}
}

func TestShowPrefixInSubdir(t *testing.T) {
	repo := repoSetup(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := git.ShowPrefix(sub)
	if err != nil {
		t.Fatalf("ShowPrefix: %v", err)
	}
	// Forward slashes, no trailing slash.
	if got != "a/b" {
		t.Errorf("ShowPrefix in a/b subdir = %q, want %q", got, "a/b")
	}
}

func TestWorktreeAddAndRemoveRoundTrip(t *testing.T) {
	repo := repoSetup(t)
	dest := filepath.Join(t.TempDir(), "wt")

	if err := git.WorktreeAdd(repo, dest, "main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	// The worktree must exist as a real dir with the README.
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("worktree README missing: %v", err)
	}
	// And tear it down. WorktreeRemove swallows errors but should
	// drop the .git/worktrees registry entry; we verify by listing.
	git.WorktreeRemove(repo, dest)
	out, _ := git.Run(repo, "worktree", "list")
	if strings.Contains(out, dest) {
		t.Errorf("worktree still registered after Remove: %s", out)
	}
}

func TestWorktreeAddNonEmptyDestIsError(t *testing.T) {
	repo := repoSetup(t)
	// git tolerates an empty existing dir on some platforms but
	// always refuses a non-empty one.
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "occupied.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.WorktreeAdd(repo, dest, "main"); err == nil {
		t.Error("WorktreeAdd should fail when dest is non-empty")
	}
}

func TestRunErrorIncludesCommandAndOutput(t *testing.T) {
	repo := repoSetup(t)
	_, err := git.Run(repo, "no-such-subcommand")
	if err == nil {
		t.Fatal("expected error from unknown subcommand")
	}
	if !strings.Contains(err.Error(), "no-such-subcommand") {
		t.Errorf("error should include the subcommand: %v", err)
	}
}
