package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/analysis"
)

// WriteUnused emits the unused subcommand's text result: a header
// counting how many entities the module declares but never references,
// followed by one ID per line. Empty input writes the "no unreferenced
// entities" baseline.
func WriteUnused(w io.Writer, unused []analysis.Entity) {
	if len(unused) == 0 {
		fmt.Fprintln(w, "No unreferenced entities found.")
		return
	}
	fmt.Fprintf(w, "Unreferenced entities (%d):\n", len(unused))
	for _, e := range unused {
		fmt.Fprintf(w, "  %s\n", e.ID())
	}
}
