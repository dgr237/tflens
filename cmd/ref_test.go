package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// ---- unit tests on pairModuleCalls (pure logic) -----------------------

// fakeProject builds a minimal loader.Project whose root module exposes
// the given module calls and whose Children map holds the listed
// children. calls is keyed by call name → (source, version). children
// keys should align with call names where a child is actually loaded.
func fakeProject(t *testing.T, calls map[string][2]string, children map[string]*loader.ModuleNode) *loader.Project {
	t.Helper()
	dir := t.TempDir()
	// Build a synthetic main.tf whose module blocks match calls.
	var b strings.Builder
	for name, sv := range calls {
		b.WriteString("module \"" + name + "\" {\n")
		b.WriteString("  source = \"" + sv[0] + "\"\n")
		if sv[1] != "" {
			b.WriteString("  version = \"" + sv[1] + "\"\n")
		}
		b.WriteString("}\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	mod, _, err := loader.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if children == nil {
		children = map[string]*loader.ModuleNode{}
	}
	return &loader.Project{Root: &loader.ModuleNode{
		Dir:      dir,
		Module:   mod,
		Children: children,
	}}
}

func TestPairModuleCallsChangedAddedRemoved(t *testing.T) {
	old := fakeProject(t,
		map[string][2]string{
			"same":    {"./sameSrc", "1.0.0"},
			"bumped":  {"ns/foo/aws", "1.0.0"},
			"retired": {"./gone", ""},
		}, nil)
	new_ := fakeProject(t,
		map[string][2]string{
			"same":   {"./sameSrc", "1.0.0"},
			"bumped": {"ns/foo/aws", "2.0.0"},
			"fresh":  {"./new", ""},
		}, nil)

	byName := map[string]modulePair{}
	for _, p := range pairModuleCalls(old, new_) {
		byName[p.key] = p
	}

	if p, ok := byName["same"]; !ok || p.status != statusChanged {
		t.Errorf("same: got %+v", p)
	}
	if p, ok := byName["bumped"]; !ok || p.status != statusChanged {
		t.Errorf("bumped: got %+v", p)
	} else if p.oldVersion != "1.0.0" || p.newVersion != "2.0.0" {
		t.Errorf("bumped versions: %+v", p)
	}
	if p, ok := byName["retired"]; !ok || p.status != statusRemoved {
		t.Errorf("retired: got %+v", p)
	}
	if p, ok := byName["fresh"]; !ok || p.status != statusAdded {
		t.Errorf("fresh: got %+v", p)
	}
}

func TestPairModuleCallsEmptyProjects(t *testing.T) {
	if got := pairModuleCalls(
		&loader.Project{Root: &loader.ModuleNode{Module: &analysis.Module{}}},
		&loader.Project{Root: &loader.ModuleNode{Module: &analysis.Module{}}},
	); len(got) != 0 {
		t.Errorf("empty projects should pair to zero calls, got %d", len(got))
	}
}

// ---- integration: build binary, run against a real local git repo -----

// buildTflens compiles the binary once per test binary run.
func buildTflens(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), binName())
	cmd := exec.Command("go", "build", "-o", bin, "github.com/dgr237/tflens")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build tflens: %v\n%s", err, out)
	}
	return bin
}

func binName() string {
	if runtime.GOOS == "windows" {
		return "tflens-e2e.exe"
	}
	return "tflens-e2e"
}

// makeRefTestRepo creates a git repo containing a workspace with a
// local-source child module. On branch `main` the child declares
// `variable "x" { type = string }` (required). On branch `feature`
// the child declares `variable "y" { type = string }` (required) and
// `x` is gone — an API-breaking change. The returned path is the
// workspace dir inside the repo, currently checked out to feature.
func makeRefTestRepo(t *testing.T) string {
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

	parent := filepath.Join(repo, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(parent, "main.tf"),
		"module \"child\" {\n  source = \"./child\"\n  x      = \"initial\"\n}\n")
	write(filepath.Join(child, "variables.tf"),
		"variable \"x\" {\n  type = string\n}\n")

	run("add", ".")
	run("commit", "--quiet", "-m", "main version")

	run("checkout", "-q", "-b", "feature")

	// Breaking change on feature: child renames its required var.
	write(filepath.Join(child, "variables.tf"),
		"variable \"y\" {\n  type = string\n}\n")
	// Update the caller to keep the workspace internally consistent.
	write(filepath.Join(parent, "main.tf"),
		"module \"child\" {\n  source = \"./child\"\n  y      = \"feature\"\n}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: rename x → y")

	return parent
}

// makeRefTestRepo updates the parent's call atomically (passes y instead
// of x), so under the consumer-view rule for local-source children
// `diff` should report NO breaking change — the parent's usage is
// consistent with the new child's API.
func TestDiffRefLocalChangeWithAtomicUpdateIsNotBreaking(t *testing.T) {
	ws := makeRefTestRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("expected exit 0 (parent's usage is consistent), got err=%v\nstderr=%s", err, stderr.String())
	}

	var out refJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	for _, m := range out.Modules {
		if m.Summary.Breaking != 0 {
			t.Errorf("module %s: expected no breaking changes for local-source child with atomic parent update, got summary=%+v changes=%+v",
				m.Name, m.Summary, m.Changes)
		}
	}
}

// makeRefTestRepoBrokenLocal builds a variant of makeRefTestRepo where
// the child renames its required var x → y but the parent forgets to
// update. Under the consumer-view rule the cross-validation finds two
// problems: the parent passes an unknown arg "x" AND fails to pass
// required "y". Both surface as Breaking.
func makeRefTestRepoBrokenLocal(t *testing.T) string {
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

	parent := filepath.Join(repo, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(parent, "main.tf"),
		"module \"child\" {\n  source = \"./child\"\n  x      = \"initial\"\n}\n")
	write(filepath.Join(child, "variables.tf"),
		"variable \"x\" {\n  type = string\n}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "main version")
	run("checkout", "-q", "-b", "feature")
	// Child renamed x → y, but parent still passes x.
	write(filepath.Join(child, "variables.tf"),
		"variable \"y\" {\n  type = string\n}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: rename x → y, parent NOT updated")
	return parent
}

// makeRefTestRepoOutputRef builds a repo where the child's output is
// renamed AND the parent's reference is updated atomically — should be
// non-breaking for diff (and vice-versa for the broken case).
func makeRefTestRepoOutputRef(t *testing.T, parentUpdated bool) string {
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
	parent := filepath.Join(repo, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// main: child exposes output "id", parent uses module.child.id.
	write(filepath.Join(parent, "main.tf"),
		"module \"child\" { source = \"./child\" }\n"+
			"output \"vpc_id\" { value = module.child.id }\n")
	write(filepath.Join(child, "main.tf"),
		"output \"id\" { value = \"vpc-12345\" }\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "main")

	run("checkout", "-q", "-b", "feature")
	// feature: child renames output id → vpc_id.
	write(filepath.Join(child, "main.tf"),
		"output \"vpc_id\" { value = \"vpc-12345\" }\n")
	if parentUpdated {
		write(filepath.Join(parent, "main.tf"),
			"module \"child\" { source = \"./child\" }\n"+
				"output \"vpc_id\" { value = module.child.vpc_id }\n")
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: rename child output id → vpc_id")
	return parent
}

func TestDiffRefOutputRenamedAtomicallyIsNotBreaking(t *testing.T) {
	ws := makeRefTestRepoOutputRef(t, true)
	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected exit 0 (parent reference updated), got err=%v\nstderr=%s", err, stderr.String())
	}
	var out refJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	for _, m := range out.Modules {
		if m.Summary.Breaking != 0 {
			t.Errorf("module %s: expected no breaking changes, got %+v", m.Name, m)
		}
	}
}

func TestDiffRefOutputRemovedWhileParentReferencesIsBreaking(t *testing.T) {
	ws := makeRefTestRepoOutputRef(t, false)
	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 (parent still references removed output), got err=%v\nstderr=%s", err, stderr.String())
	}
	var out refJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	saw := false
	for _, m := range out.Modules {
		for _, c := range m.Changes {
			if strings.Contains(c.Detail, "module.child.id") && strings.Contains(c.Detail, "no such output") {
				saw = true
			}
		}
	}
	if !saw {
		t.Errorf("expected a 'module.child.id ... no such output' message, got: %+v", out.Modules)
	}
}

func TestDiffRefLocalChangeWithBrokenParentIsBreaking(t *testing.T) {
	ws := makeRefTestRepoBrokenLocal(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 (parent's usage is broken), got err=%v\nstderr=%s", err, stderr.String())
	}

	var out refJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	if len(out.Modules) != 1 {
		t.Fatalf("expected 1 module entry, got %d: %+v", len(out.Modules), out.Modules)
	}
	child := out.Modules[0]
	if child.Summary.Breaking == 0 {
		t.Errorf("expected breaking changes (parent passes unknown x, missing required y), got summary=%+v", child.Summary)
	}
	sawUnknownX, sawMissingY := false, false
	for _, c := range child.Changes {
		if strings.Contains(c.Detail, "unknown argument") && strings.Contains(c.Detail, "\"x\"") {
			sawUnknownX = true
		}
		if strings.Contains(c.Detail, "required input") && strings.Contains(c.Detail, "\"y\"") {
			sawMissingY = true
		}
	}
	if !sawUnknownX {
		t.Errorf("expected an 'unknown argument x' message, got: %+v", child.Changes)
	}
	if !sawMissingY {
		t.Errorf("expected a 'required input y' message, got: %+v", child.Changes)
	}
}

func TestWhatifRefReportsDirectImpactForAllChangedCalls(t *testing.T) {
	ws := makeRefTestRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "whatif", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// Non-zero exit expected — base parent calls x, new child no longer accepts x.
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 for direct impact, got err=%v\nstderr=%s", err, stderr.String())
	}

	var out struct {
		BaseRef string `json:"base_ref"`
		Calls   []struct {
			Name         string `json:"name"`
			Status       string `json:"status"`
			DirectImpact []struct {
				Msg string `json:"msg"`
			} `json:"direct_impact"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	if out.BaseRef != "main" {
		t.Errorf("base_ref = %q, want main", out.BaseRef)
	}
	if len(out.Calls) != 1 || out.Calls[0].Name != "child" {
		t.Fatalf("calls = %+v, want one entry for 'child'", out.Calls)
	}
	if len(out.Calls[0].DirectImpact) == 0 {
		t.Errorf("expected direct impact issues, got none")
	}
}

func TestWhatifRefByCallName(t *testing.T) {
	ws := makeRefTestRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "whatif", "--ref", "main", "--format=json", ws, "child")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		exitErr, _ := err.(*exec.ExitError)
		if exitErr == nil || exitErr.ExitCode() != 1 {
			t.Fatalf("unexpected error: %v\nstderr=%s", err, stderr.String())
		}
	}
	// JSON is pretty-printed, so match loosely.
	if !strings.Contains(stdout.String(), `"name": "child"`) {
		t.Errorf("expected JSON with name=child, got: %s", stdout.String())
	}
}

func TestWhatifRefUnknownCallName(t *testing.T) {
	ws := makeRefTestRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "whatif", "--ref", "main", ws, "nonexistent")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for unknown call name, got clean exit; output:\n%s", out)
	}
	if !strings.Contains(string(out), "nonexistent") {
		t.Errorf("error should mention the call name, got:\n%s", out)
	}
}

// makeNestedRefTestRepo builds a git repo whose workspace contains a
// root module calling "vpc", which in turn calls "sg". On branch main,
// sg has required variable "x"; on branch feature, sg's required
// variable is renamed to "y" — a breaking change nested two levels
// deep.
func makeNestedRefTestRepo(t *testing.T) string {
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

	parent := filepath.Join(repo, "workspace")
	vpc := filepath.Join(parent, "vpc")
	sg := filepath.Join(vpc, "sg")
	if err := os.MkdirAll(sg, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(parent, "main.tf"),
		"module \"vpc\" {\n  source = \"./vpc\"\n}\n")
	write(filepath.Join(vpc, "main.tf"),
		"module \"sg\" {\n  source = \"./sg\"\n  x      = \"initial\"\n}\n")
	write(filepath.Join(sg, "variables.tf"),
		"variable \"x\" {\n  type = string\n}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "main: nested vpc → sg")

	run("checkout", "-q", "-b", "feature")
	write(filepath.Join(sg, "variables.tf"),
		"variable \"y\" {\n  type = string\n}\n")
	// Update vpc to keep feature branch internally consistent.
	write(filepath.Join(vpc, "main.tf"),
		"module \"sg\" {\n  source = \"./sg\"\n  y      = \"feature\"\n}\n")
	run("add", ".")
	run("commit", "--quiet", "-m", "feature: rename sg.x → sg.y")

	return parent
}

// makeNestedRefTestRepo updates vpc → sg atomically, so under the
// consumer-view rule for local-source children `diff` reports no
// breaking change at the vpc.sg pair — the nested parent (vpc) updated
// its call to match sg's new variable name.
func TestDiffRefNestedAtomicUpdateIsNotBreaking(t *testing.T) {
	ws := makeNestedRefTestRepo(t)
	bin := buildTflens(t)

	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("expected exit 0 (nested parent updated atomically), got err=%v\nstderr=%s", err, stderr.String())
	}

	var out refJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	for _, m := range out.Modules {
		if m.Summary.Breaking != 0 {
			t.Errorf("module %s: expected no breaking changes, got summary=%+v", m.Name, m.Summary)
		}
	}
}

func TestWhatifRefByNestedCallName(t *testing.T) {
	ws := makeNestedRefTestRepo(t)
	bin := buildTflens(t)

	// Filter by dotted key vpc.sg; whatif should use the vpc module
	// (not the root) as the parent for CrossValidateCall.
	cmd := exec.Command(bin, "--offline", "whatif", "--ref", "main", "--format=json", ws, "vpc.sg")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 for direct impact on vpc.sg, got err=%v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name": "vpc.sg"`) {
		t.Errorf("expected JSON keyed by vpc.sg, got: %s", stdout.String())
	}
}

func TestDiffRefAutoDetectsBase(t *testing.T) {
	// Use the broken-local fixture so the test exercises both auto-ref
	// detection AND a non-zero exit (the parent's usage is broken under
	// the new child).
	ws := makeRefTestRepoBrokenLocal(t)
	bin := buildTflens(t)

	// --ref auto with no explicit ref should find "main" via the
	// local-branches fallback (no upstream, no origin/HEAD in this
	// freshly init'd repo).
	cmd := exec.Command(bin, "--offline", "diff", "--ref", "auto", "--format=json", ws)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit 1 (broken parent usage), got err=%v\nstderr=%s", err, stderr.String())
	}
	var out struct {
		BaseRef string `json:"base_ref"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	if out.BaseRef != "main" {
		t.Errorf("auto-detected base_ref = %q, want main", out.BaseRef)
	}
}

func TestDiffRefNoChanges(t *testing.T) {
	// Same as makeRefTestRepo but without the feature commit — just main.
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
	if err := os.WriteFile(filepath.Join(ws, "main.tf"), []byte("variable \"x\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "init")
	run("checkout", "-q", "-b", "feature")

	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", ws)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("diff --branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "No changes detected") {
		t.Errorf("expected 'No changes detected' message, got:\n%s", out)
	}
}

// TestDiffRefRootRequiredVariableAdded covers the case where the root
// module gains a new required variable. The root isn't a module call so
// it isn't paired by pairModuleCalls; without an explicit root-module
// diff pass it would go undetected. Regression test for that.
func TestDiffRefRootRequiredVariableAdded(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(ws, "main.tf"),
		[]byte(`resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "init")
	run("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(ws, "main.tf"),
		[]byte("variable \"test\" { type = string }\n"+
			`resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := buildTflens(t)
	cmd := exec.Command(bin, "--offline", "diff", "--ref", "main", ws)
	out, _ := cmd.CombinedOutput() // exits 1 on Breaking, that's expected
	s := string(out)
	if !strings.Contains(s, "Root module:") {
		t.Errorf("expected 'Root module:' header, got:\n%s", s)
	}
	if !strings.Contains(s, "variable.test") || !strings.Contains(s, "required variable added") {
		t.Errorf("expected required-variable-added on variable.test, got:\n%s", s)
	}
	if cmd.ProcessState.ExitCode() != 1 {
		t.Errorf("expected exit 1 for Breaking root change, got %d", cmd.ProcessState.ExitCode())
	}
}
