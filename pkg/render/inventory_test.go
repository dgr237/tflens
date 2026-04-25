package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
)

// inventoryFixtureModule loads
// pkg/render/testdata/inventory/<case>/main.tf as an analysed Module.
// Lets the inventory test cases keep their HCL alongside other
// renderer fixtures rather than as inline raw-string Go literals.
func inventoryFixtureModule(t *testing.T, name string) *analysis.Module {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", "inventory", name, "main.tf")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	p := hclparse.NewParser()
	f, diags := p.ParseHCL(src, "main.tf")
	if diags.HasErrors() {
		t.Fatalf("parse %s: %v", path, diags)
	}
	body, _ := f.Body.(*hclsyntax.Body)
	return analysis.Analyse(&analysis.File{Filename: "main.tf", Source: src, Body: body})
}

// inventoryCase pairs a fixture name with assertions on the rendered
// inventory output. Output is captured into a bytes.Buffer so the
// Custom func can scan substrings, lengths, ordering, etc.
type inventoryCase struct {
	Name   string
	Custom func(t *testing.T, out string)
}

func TestRendererInventoryCases(t *testing.T) {
	for _, tc := range inventoryCases {
		t.Run(tc.Name, func(t *testing.T) {
			mod := inventoryFixtureModule(t, tc.Name)
			var b bytes.Buffer
			consoleRenderer(&b).Inventory(mod)
			tc.Custom(t, b.String())
		})
	}
}

var inventoryCases = []inventoryCase{
	{
		// Section order must be Variables → Locals → Data sources →
		// Resources → Modules → Outputs (matches the legacy cmd output).
		// Fixture has one entity per kind so each section header reads
		// "(1)".
		Name: "section_order_and_counts",
		Custom: func(t *testing.T, out string) {
			if !strings.HasPrefix(out, "Entities: 6\n") {
				t.Errorf("missing total header; got:\n%s", out)
			}
			wantOrder := []string{
				"Variables (1):",
				"Locals (1):",
				"Data sources (1):",
				"Resources (1):",
				"Modules (1):",
				"Outputs (1):",
			}
			prev := -1
			for _, h := range wantOrder {
				i := strings.Index(out, h)
				if i < 0 {
					t.Errorf("missing section %q in:\n%s", h, out)
					continue
				}
				if i < prev {
					t.Errorf("section %q out of order in:\n%s", h, out)
				}
				prev = i
			}
		},
	},
	{
		// Fixture is a single variable; every other kind's section
		// must be omitted (no header / no count line).
		Name: "skips_empty_kinds",
		Custom: func(t *testing.T, out string) {
			for _, absent := range []string{"Locals", "Data sources", "Resources", "Modules", "Outputs"} {
				if strings.Contains(out, absent) {
					t.Errorf("section %q should be omitted when empty; got:\n%s", absent, out)
				}
			}
		},
	},
}
