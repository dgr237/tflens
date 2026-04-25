package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
)

func TestRendererFmtParseErrors(t *testing.T) {
	// Generate real diagnostics via hclparse so the format mirrors what
	// `tflens fmt` actually surfaces in production.
	p := hclparse.NewParser()
	_, diags := p.ParseHCL([]byte(`resource "missing-second-label" {`), "broken.tf")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics from broken HCL")
	}
	var b bytes.Buffer
	consoleRenderer(&b).FmtParseErrors(diags)
	out := b.String()
	if !strings.HasPrefix(out, "parse error: ") {
		t.Errorf("expected leading 'parse error:'; got %q", out)
	}
	if !strings.Contains(out, "broken.tf") {
		t.Errorf("expected filename in output; got %q", out)
	}
}

func TestRendererFmtParseErrorsEmpty(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).FmtParseErrors(nil)
	if got := b.String(); got != "" {
		t.Errorf("nil diags should produce no output; got %q", got)
	}
}
