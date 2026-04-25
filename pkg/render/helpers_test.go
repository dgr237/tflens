package render_test

import (
	"io"

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
