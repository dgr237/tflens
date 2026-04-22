package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeStatediffRepo creates a git repo whose workspace has:
//
//   - A local.regions containing ["us-east-1", "us-west-2"] on main and
//     ["us-east-1"] on feature — a deletion-inducing change.
//   - A resource aws_instance.web with for_each = toset(local.regions).
//   - An unrelated resource aws_vpc.main on main, dropped on feature.
func makeStatediffRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")

	ws := filepath.Join(repo, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(ws, "main.tf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`locals {
  regions = ["us-east-1", "us-west-2"]
}

resource "aws_instance" "web" {
  for_each = toset(local.regions)
}

resource "aws_vpc" "main" {
}
`)
	run("add", ".")
	run("commit", "--quiet", "-m", "main: two regions + vpc")

	run("checkout", "-q", "-b", "feature")
	write(`locals {
  regions = ["us-east-1"]
}

resource "aws_instance" "web" {
  for_each = toset(local.regions)
}
`)
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: drop us-west-2 + drop vpc")
	return ws
}

func writeStateFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatediffDetectsLocalInducedDeletion(t *testing.T) {
	ws := makeStatediffRepo(t)
	bin := buildTflens(t)

	statePath := writeStateFile(t, `{
  "version": 4,
  "resources": [
    {
      "module": "",
      "mode": "managed",
      "type": "aws_instance",
      "name": "web",
      "instances": [
        {"index_key": "us-east-1", "attributes": {}},
        {"index_key": "us-west-2", "attributes": {}}
      ]
    },
    {
      "module": "",
      "mode": "managed",
      "type": "aws_vpc",
      "name": "main",
      "instances": [{"attributes": {}}]
    }
  ]
}`)

	cmd := exec.Command(bin, "--offline", "statediff", "--ref", "main", "--state", statePath, "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got err=%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}

	var out statediffResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}

	// Resource removal: aws_vpc.main.
	if len(out.RemovedResources) == 0 {
		t.Errorf("expected aws_vpc.main to be reported as removed, got: %+v", out.RemovedResources)
	} else {
		found := false
		for _, r := range out.RemovedResources {
			if r.Type == "aws_vpc" && r.Name == "main" {
				found = true
			}
		}
		if !found {
			t.Errorf("aws_vpc.main not in removed list: %+v", out.RemovedResources)
		}
	}

	// Locals sensitivity on aws_instance.web's for_each.
	if len(out.SensitiveChanges) != 1 {
		t.Fatalf("expected 1 sensitive local, got %d: %+v", len(out.SensitiveChanges), out.SensitiveChanges)
	}
	sl := out.SensitiveChanges[0]
	if sl.Name != "regions" {
		t.Errorf("sensitive local name = %q, want regions", sl.Name)
	}
	if len(sl.AffectedResources) != 1 {
		t.Fatalf("expected 1 affected resource, got %d", len(sl.AffectedResources))
	}
	ar := sl.AffectedResources[0]
	if ar.Type != "aws_instance" || ar.Name != "web" {
		t.Errorf("affected resource = %+v, want aws_instance.web", ar)
	}
	if ar.MetaArg != "for_each" {
		t.Errorf("meta arg = %q, want for_each", ar.MetaArg)
	}
	if len(ar.StateInstances) != 2 {
		t.Errorf("expected 2 state instances, got %d: %v", len(ar.StateInstances), ar.StateInstances)
	}
	foundUsWest := false
	for _, s := range ar.StateInstances {
		if strings.Contains(s, `"us-west-2"`) {
			foundUsWest = true
		}
	}
	if !foundUsWest {
		t.Errorf("state instances should include us-west-2, got: %v", ar.StateInstances)
	}
}

func TestStatediffWithoutStateStillFlagsLocal(t *testing.T) {
	ws := makeStatediffRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "statediff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got err=%v\nstderr=%s", err, stderr.String())
	}
	var out statediffResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.SensitiveChanges) != 1 {
		t.Errorf("expected sensitive local without state, got: %+v", out.SensitiveChanges)
	}
	if ar := out.SensitiveChanges[0].AffectedResources[0]; len(ar.StateInstances) != 0 {
		t.Errorf("StateInstances should be empty without --state, got: %v", ar.StateInstances)
	}
}

func TestStatediffRecognisesMovedBlockRename(t *testing.T) {
	// A resource rename via `moved { from = ..., to = ... }` must NOT
	// appear as a remove + add. It should be listed as a rename (which
	// does not contribute to the exit code).
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	ws := filepath.Join(repo, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(ws, "main.tf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`resource "aws_vpc" "old" {}`)
	run("add", ".")
	run("commit", "--quiet", "-m", "main")
	run("checkout", "-q", "-b", "feature")
	write(`resource "aws_vpc" "new" {}
moved {
  from = aws_vpc.old
  to   = aws_vpc.new
}`)
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: rename via moved")

	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "statediff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("statediff should exit 0 on a moved-handled rename, got %v\nstderr=%s\nstdout=%s",
			err, stderr.String(), stdout.String())
	}
	var out statediffResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.AddedResources) != 0 || len(out.RemovedResources) != 0 {
		t.Errorf("expected no add/remove, got added=%+v removed=%+v", out.AddedResources, out.RemovedResources)
	}
	if len(out.RenamedResources) != 1 {
		t.Fatalf("expected 1 rename, got %+v", out.RenamedResources)
	}
	r := out.RenamedResources[0]
	if r.From != "resource.aws_vpc.old" || r.To != "resource.aws_vpc.new" {
		t.Errorf("rename = %+v", r)
	}
}

func TestStatediffDetectsVariableDefaultChange(t *testing.T) {
	// Same mechanism as sensitive locals, but the trigger is a variable
	// default whose value reaches a count expression.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	ws := filepath.Join(repo, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(ws, "main.tf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`variable "n" {
  type    = number
  default = 3
}
resource "aws_instance" "web" {
  count = var.n
}`)
	run("add", ".")
	run("commit", "--quiet", "-m", "main")
	run("checkout", "-q", "-b", "feature")
	write(`variable "n" {
  type    = number
  default = 1
}
resource "aws_instance" "web" {
  count = var.n
}`)
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: shrink n")

	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "statediff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got err=%v\nstderr=%s", err, stderr.String())
	}
	var out statediffResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.SensitiveChanges) != 1 {
		t.Fatalf("expected 1 sensitive change, got %+v", out.SensitiveChanges)
	}
	sc := out.SensitiveChanges[0]
	if sc.Kind != "variable" {
		t.Errorf("kind = %q, want variable", sc.Kind)
	}
	if sc.Name != "n" {
		t.Errorf("name = %q, want n", sc.Name)
	}
	if strings.TrimSpace(sc.OldValue) != "3" || strings.TrimSpace(sc.NewValue) != "1" {
		t.Errorf("values: old=%q new=%q", sc.OldValue, sc.NewValue)
	}
	if len(sc.AffectedResources) != 1 || sc.AffectedResources[0].MetaArg != "count" {
		t.Errorf("affected = %+v", sc.AffectedResources)
	}
}

func TestStatediffCleanExitOnNoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	ws := filepath.Join(repo, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "main.tf"),
		[]byte("resource \"aws_vpc\" \"main\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "init")
	run("checkout", "-q", "-b", "feature")

	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "statediff", "--ref", "main", ws)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("statediff: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "No resource identity or sensitive-local changes") {
		t.Errorf("expected clean message, got:\n%s", out)
	}
}
