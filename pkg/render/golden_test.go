package render_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// updateGoldens regenerates testdata/<group>/<name>.golden files from
// the actual rendered output. Run with `go test ./pkg/render/...
// -update` after changing renderer formatting; review the diff before
// committing.
var updateGoldens = flag.Bool("update", false, "regenerate testdata golden files")

// checkGolden compares got against testdata/<group>/<name>.golden.
// When -update is passed the golden file is rewritten and the test
// passes; otherwise a mismatch produces a unified-style error.
//
// Centralised here so every renderer's table-driven cases share the
// same -update flag and path layout.
func checkGolden(t *testing.T, group, name string, got []byte) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", group, name+".golden")
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
	if !bytes.Equal(got, want) {
		t.Errorf("output mismatch for %s/%s.golden\n--- want ---\n%s\n--- got ---\n%s",
			group, name, want, got)
	}
}
