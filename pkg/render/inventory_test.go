package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
)

// inventoryFromSrc builds an analysis.Module from inline HCL so the
// inventory tests can assert section ordering and entity formatting
// without needing a fixture directory for each shape.
func inventoryFromSrc(t *testing.T, src string) *analysis.Module {
	t.Helper()
	p := hclparse.NewParser()
	f, diags := p.ParseHCL([]byte(src), "test.tf")
	if diags.HasErrors() {
		t.Fatalf("parse: %v", diags)
	}
	body, _ := f.Body.(*hclsyntax.Body)
	return analysis.Analyse(&analysis.File{Filename: "test.tf", Source: []byte(src), Body: body})
}

func TestRendererInventorySectionOrderAndCounts(t *testing.T) {
	mod := inventoryFromSrc(t, `
variable "v" { type = string }
locals { l = 1 }
data "aws_ami" "u" {}
resource "aws_vpc" "main" {}
module "m" { source = "./x" }
output "o" { value = 1 }
`)
	var b bytes.Buffer
	consoleRenderer(&b).Inventory(mod)
	out := b.String()
	if !strings.HasPrefix(out, "Entities: 6\n") {
		t.Errorf("missing total header; got:\n%s", out)
	}
	// Section order must be Variables → Locals → Data sources → Resources
	// → Modules → Outputs (matches the legacy cmd output).
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
}

func TestRendererInventorySkipsEmptyKinds(t *testing.T) {
	mod := inventoryFromSrc(t, `variable "v" { type = string }`)
	var b bytes.Buffer
	consoleRenderer(&b).Inventory(mod)
	out := b.String()
	for _, absent := range []string{"Locals", "Data sources", "Resources", "Modules", "Outputs"} {
		if strings.Contains(out, absent) {
			t.Errorf("section %q should be omitted when empty; got:\n%s", absent, out)
		}
	}
}
