package render_test

import (
	"io"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

// consoleRenderer returns a text-mode render.Renderer that writes to
// w. Used by tests that previously called the package-private writeX
// helpers directly — they now drive through the public Renderer API.
func consoleRenderer(w io.Writer) render.Renderer {
	return render.New(config.Settings{Out: w})
}

// jsonRenderer returns a JSON-mode render.Renderer that writes to w.
// Tests can json.Unmarshal w.Bytes() into the public envelope types
// (JSONDiffOutput, etc.) to assert the wire format.
func jsonRenderer(w io.Writer) render.Renderer {
	return render.New(config.Settings{Out: w, JSON: true})
}

// markdownRenderer returns a markdown-mode render.Renderer that
// writes to w. Pairs with the per-case .golden.md files under
// testdata/markdown/ for golden-style assertion of the rendered
// output.
func markdownRenderer(w io.Writer) render.Renderer {
	return render.New(config.Settings{Out: w, Markdown: true})
}

// moduleFromSrc builds an analysis.Module from inline HCL. Used by
// the json_test wire-format pinning tests where the input is a
// one-line resource / data declaration that doesn't justify a
// fixture directory of its own.
func moduleFromSrc(t *testing.T, src string) *analysis.Module {
	t.Helper()
	p := hclparse.NewParser()
	f, diags := p.ParseHCL([]byte(src), "test.tf")
	if diags.HasErrors() {
		t.Fatalf("parse: %v", diags)
	}
	body, _ := f.Body.(*hclsyntax.Body)
	return analysis.Analyse(&analysis.File{Filename: "test.tf", Source: []byte(src), Body: body})
}
