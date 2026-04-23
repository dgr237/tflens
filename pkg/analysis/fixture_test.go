package analysis_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
)

// analyseFixture parses src under "test.tf" and returns the analysis Module.
// Convenience wrapper around analyseFixtureNamed.
func analyseFixture(t *testing.T, src string) *analysis.Module {
	return analyseFixtureNamed(t, "test.tf", src)
}

// analyseFixtureNamed parses src under filename and returns the analysis
// Module. Parse errors fail the test.
func analyseFixtureNamed(t *testing.T, filename, src string) *analysis.Module {
	t.Helper()
	return analysis.Analyse(parseToFile(t, filename, src))
}

// parseToFile parses src to a hclsyntax body and wraps it in an analysis.File.
// Parse errors fail the test.
func parseToFile(t *testing.T, filename, src string) *analysis.File {
	t.Helper()
	p := hclparse.NewParser()
	hclFile, diags := p.ParseHCL([]byte(src), filename)
	for _, d := range diags {
		t.Errorf("parse error: %s", d.Error())
	}
	if t.Failed() {
		t.FailNow()
	}
	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("unexpected body type %T", hclFile.Body)
	}
	return &analysis.File{Filename: filename, Source: []byte(src), Body: body}
}

// loadAnalysisFixture reads testdata/<group>/<name>/main.tf and returns its
// contents as a string. Empty when the file doesn't exist.
func loadAnalysisFixture(t *testing.T, group, name string) string {
	t.Helper()
	path := filepath.Join("testdata", group, name, "main.tf")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
