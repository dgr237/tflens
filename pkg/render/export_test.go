package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

// exportFixtureProject loads pkg/render/testdata/export/<name> as a
// loader.Project using the offline-only loader (no network). Mirrors
// the inventoryFixtureModule pattern but for full project trees so
// child-module nesting fixtures are supported.
func exportFixtureProject(t *testing.T, name string) *loader.Project {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata", "export", name)
	p, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject(%s): %v", name, err)
	}
	return p
}

// TestRendererExportCases runs every fixture under
// pkg/render/testdata/export/<case>/ through BuildExport + WriteExport
// and matches the result against the case's output.json. Per-case
// expected output co-located with the input keeps everything one
// directory away from the failure.
//
// Updating goldens: `go test ./pkg/render/... -run
// TestRendererExportCases -update` rewrites every output.json from
// the actual rendered bytes. Review the diff before committing — the
// shape is experimental but a change to the golden IS a change to
// the contract that downstream converters depend on.
func TestRendererExportCases(t *testing.T) {
	for _, name := range exportCaseNames {
		t.Run(name, func(t *testing.T) {
			p := exportFixtureProject(t, name)
			exp := render.BuildExport(p, "test-version")
			normalizeForGolden(&exp)

			var buf bytes.Buffer
			if err := render.WriteExport(exp, &buf); err != nil {
				t.Fatalf("WriteExport: %v", err)
			}
			checkExportGolden(t, name, buf.Bytes())
		})
	}
}

// exportCaseNames lists each subdirectory under testdata/export/ that
// the test should drive. One name → one fixture (main.tf and any
// children) plus one expected output.json sitting beside it.
var exportCaseNames = []string{
	"scalar_entities",
	"resources_and_data",
	"terraform_block",
	"nested_modules",
	"resource_attributes",
	"nested_blocks_eks",
}

// TestBuildExportNilProjectIsEmpty pins the nil-safety contract: a
// nil/empty project produces a valid envelope (with experimental flag
// + schema version) rather than panicking. Inline because no fixture
// is needed — the input is literally nil.
func TestBuildExportNilProjectIsEmpty(t *testing.T) {
	exp := render.BuildExport(nil, "")
	if !exp.Experimental {
		t.Error("nil project should still emit envelope with _experimental")
	}
	if exp.SchemaVersion == "" {
		t.Error("nil project should still emit schema_version")
	}
}

// ---- helpers ----

// normalizeForGolden rewrites the absolute Dir paths on every node in
// the tree to just the directory basename. The absolute path is
// machine-specific (e.g. C:\wip\tflens\... vs /home/user/...) so a
// raw golden would only ever match on one developer's machine. The
// basename keeps the structural shape intact (each child still nests
// under its module-call name with the right sub-directory).
func normalizeForGolden(exp *render.Export) {
	normalizeNode(&exp.Root)
}

func normalizeNode(n *render.ExportNode) {
	if n.Dir != "" {
		n.Dir = filepath.Base(n.Dir)
	}
	for name, child := range n.Children {
		normalizeNode(&child)
		n.Children[name] = child
	}
}

// checkExportGolden compares got against
// testdata/export/<name>/output.json. When -update is passed (the
// flag from golden_test.go is shared) the file is rewritten and the
// test passes; otherwise a mismatch produces a diff-style error.
//
// Per-case output.json (vs the flat <name>.golden layout used by the
// other renderer tests) keeps each fixture self-contained: input +
// expected output sit next to each other under one directory.
func checkExportGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", "export", name, "output.json")
	if *updateGoldens {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n(rerun with -update to create it)", path, err)
	}
	// Normalise CRLF → LF so Windows checkouts that auto-converted the
	// committed golden don't spuriously mismatch the LF-style emit.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("output mismatch for export/%s/output.json\n--- want ---\n%s\n--- got ---\n%s",
			name, want, got)
	}
}
