package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/statediff"
)

// statediffCase pairs a fixture (under cmd/testdata/statediff/<Name>)
// with assertions on the binary's output. Each fixture provides
// main/main.tf (committed on `main`) and feature/main.tf (committed
// on `feature`); the case may also drop a state.json file alongside
// to opt into --state.
//
// Adding a case: drop the fixture under testdata/statediff/<name>/
// + append a struct entry. UseTextMode=true skips --format=json so
// the assertion can scan the human-readable baseline message.
type statediffCase struct {
	Name        string
	Fixture     string // testdata subdir; defaults to Name when empty
	UseTextMode bool   // when true, omit --format=json
	StateFile   string // when non-empty, pass --state pointing at testdata/<fixture>/<StateFile>
	WantExit    int    // expected process exit code
	Custom      func(t *testing.T, stdout, stderr []byte)
}

// fixtureDirName returns Fixture if set, otherwise Name. Lets two
// cases share a fixture (e.g. with/without --state).
func (c statediffCase) fixtureDirName() string {
	if c.Fixture != "" {
		return c.Fixture
	}
	return c.Name
}

func TestStatediffCases(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bin := buildTflens(t)
	for _, tc := range statediffCases {
		t.Run(tc.Name, func(t *testing.T) {
			fixture := tc.fixtureDirName()
			ws := buildStatediffRepo(t, fixture)
			args := []string{"--offline", "statediff", "--ref", "main"}
			if tc.StateFile != "" {
				args = append(args, "--state", statediffFixturePath(t, fixture, tc.StateFile))
			}
			if !tc.UseTextMode {
				args = append(args, "--format=json")
			}
			args = append(args, ws)
			stdout, stderr, exitCode := runBinary(t, bin, args...)
			if exitCode != tc.WantExit {
				t.Fatalf("exit code = %d, want %d\nstdout=%s\nstderr=%s",
					exitCode, tc.WantExit, stdout, stderr)
			}
			tc.Custom(t, stdout, stderr)
		})
	}
}

var statediffCases = []statediffCase{
	{
		// Local list shrinks (us-west-2 dropped) AND a top-level resource
		// removed. With --state, the affected aws_instance.web records
		// its 2 known instances so reviewers see the concrete addresses.
		Name:      "local_induced_deletion",
		StateFile: "state.json",
		WantExit:  1,
		Custom: func(t *testing.T, stdout, _ []byte) {
			out := unmarshalStatediff(t, stdout)
			if !hasRemoved(out, "aws_vpc", "main") {
				t.Errorf("expected aws_vpc.main in removed list: %+v", out.RemovedResources)
			}
			if len(out.SensitiveChanges) != 1 {
				t.Fatalf("expected 1 sensitive local, got %+v", out.SensitiveChanges)
			}
			sl := out.SensitiveChanges[0]
			if sl.Name != "regions" {
				t.Errorf("sensitive name = %q, want regions", sl.Name)
			}
			if len(sl.AffectedResources) != 1 || sl.AffectedResources[0].MetaArg != "for_each" {
				t.Errorf("affected = %+v", sl.AffectedResources)
			}
			if got := sl.AffectedResources[0].StateInstances; len(got) != 2 ||
				!containsAny(got, `"us-west-2"`) {
				t.Errorf("StateInstances should include us-west-2, got %v", got)
			}
		},
	},
	{
		// Same fixture as above, but no --state — StateInstances on the
		// affected resource must be empty (the analyser still flags the
		// sensitive change without state cross-reference).
		Name:     "local_induced_deletion_no_state",
		Fixture:  "local_induced_deletion", // share fixture with the with-state case
		WantExit: 1,
		Custom: func(t *testing.T, stdout, _ []byte) {
			out := unmarshalStatediff(t, stdout)
			if len(out.SensitiveChanges) != 1 {
				t.Errorf("expected 1 sensitive local, got %+v", out.SensitiveChanges)
			}
			if got := out.SensitiveChanges[0].AffectedResources[0].StateInstances; len(got) != 0 {
				t.Errorf("StateInstances should be empty without --state, got %v", got)
			}
		},
	},
	{
		// A resource rename via `moved {}` block: must NOT appear as
		// remove + add. Goes in the rename list, which doesn't
		// contribute to the exit code → exit 0.
		Name:     "moved_block_rename",
		WantExit: 0,
		Custom: func(t *testing.T, stdout, _ []byte) {
			out := unmarshalStatediff(t, stdout)
			if len(out.AddedResources) != 0 || len(out.RemovedResources) != 0 {
				t.Errorf("expected no add/remove, got added=%+v removed=%+v",
					out.AddedResources, out.RemovedResources)
			}
			if len(out.RenamedResources) != 1 {
				t.Fatalf("expected 1 rename, got %+v", out.RenamedResources)
			}
			r := out.RenamedResources[0]
			if r.From != "resource.aws_vpc.old" || r.To != "resource.aws_vpc.new" {
				t.Errorf("rename = %+v", r)
			}
		},
	},
	{
		// Variable default 3 → 1 with count = var.n: same mechanism as
		// sensitive locals, but the trigger is a variable default.
		Name:     "variable_default_change",
		WantExit: 1,
		Custom: func(t *testing.T, stdout, _ []byte) {
			out := unmarshalStatediff(t, stdout)
			if len(out.SensitiveChanges) != 1 {
				t.Fatalf("expected 1 sensitive change, got %+v", out.SensitiveChanges)
			}
			sc := out.SensitiveChanges[0]
			if sc.Kind != "variable" || sc.Name != "n" {
				t.Errorf("change = %+v, want variable.n", sc)
			}
			if strings.TrimSpace(sc.OldValue) != "3" || strings.TrimSpace(sc.NewValue) != "1" {
				t.Errorf("values: old=%q new=%q", sc.OldValue, sc.NewValue)
			}
			if len(sc.AffectedResources) != 1 || sc.AffectedResources[0].MetaArg != "count" {
				t.Errorf("affected = %+v", sc.AffectedResources)
			}
		},
	},
	{
		// main and feature branches identical → no findings, text
		// output emits the "no resource identity or sensitive-local
		// changes" baseline. Text mode (no --format=json).
		Name:        "clean_no_changes",
		UseTextMode: true,
		WantExit:    0,
		Custom: func(t *testing.T, stdout, _ []byte) {
			if !strings.Contains(string(stdout), "No resource identity or sensitive-local changes") {
				t.Errorf("expected clean message, got:\n%s", stdout)
			}
		},
	},
}

// ---- helpers ----

// buildStatediffRepo materialises a statediff fixture as a real git
// repo: main/main.tf committed on `main`, feature/main.tf committed
// on `feature`. The workspace lives at <repo>/workspace so the
// per-call paths produced by `git rev-parse --show-prefix` exercise
// the prefix logic (rather than the trivial empty-prefix case).
func buildStatediffRepo(t *testing.T, caseName string) string {
	t.Helper()
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "--quiet", "-b", "main")
	ws := filepath.Join(repo, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspace(t, ws, statediffFixtureRead(t, caseName, "main", "main.tf"))
	gitInRepo(t, repo, "add", ".")
	gitInRepo(t, repo, "commit", "--quiet", "-m", "main: "+caseName)

	gitInRepo(t, repo, "checkout", "-q", "-b", "feature")
	writeWorkspace(t, ws, statediffFixtureRead(t, caseName, "feature", "main.tf"))
	gitInRepo(t, repo, "add", ".")
	// --allow-empty so a clean_no_changes-style fixture (feature ==
	// main) doesn't fail the commit. The branch still exists for
	// statediff to compare against.
	gitInRepo(t, repo, "commit", "--quiet", "--allow-empty", "-m", "feature: "+caseName)
	return ws
}

// statediffFixturePath returns the absolute path to a file under
// cmd/testdata/statediff/<case>/<rel...>.
func statediffFixturePath(t *testing.T, caseName string, rel ...string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	parts := append([]string{filepath.Dir(file), "testdata", "statediff", caseName}, rel...)
	return filepath.Join(parts...)
}

// statediffFixtureRead reads a fixture file and returns its bytes.
func statediffFixtureRead(t *testing.T, caseName string, rel ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(statediffFixturePath(t, caseName, rel...))
	if err != nil {
		t.Fatalf("read fixture %s/%v: %v", caseName, rel, err)
	}
	return b
}

func writeWorkspace(t *testing.T, dir string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitInRepo runs `git <args...>` in repo with deterministic author
// metadata so commits are reproducible across runners.
func gitInRepo(t *testing.T, repo string, args ...string) {
	t.Helper()
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

// runBinary runs the compiled tflens binary with args and returns
// its stdout, stderr, and exit code (0 on success). Doesn't fail the
// test on non-zero exit — callers compare against tc.WantExit.
func runBinary(t *testing.T, bin string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s %v: %v", bin, args, err)
	}
	return stdout.Bytes(), stderr.Bytes(), code
}

func unmarshalStatediff(t *testing.T, raw []byte) statediff.Result {
	t.Helper()
	var out statediff.Result
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
	}
	return out
}

func hasRemoved(r statediff.Result, ty, name string) bool {
	for _, x := range r.RemovedResources {
		if x.Type == ty && x.Name == name {
			return true
		}
	}
	return false
}

func containsAny(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
