package render

import (
	"fmt"
	"io"
	"strings"
)

// writeCycles emits the human-readable result of a Module.Cycles()
// call. Empty input writes the "no cycles" baseline; otherwise emits
// a numbered list with the cycle members joined by " → ".
func writeCycles(w io.Writer, cycles [][]string) {
	if len(cycles) == 0 {
		fmt.Fprintln(w, "No cycles detected.")
		return
	}
	fmt.Fprintf(w, "Cycles detected (%d):\n", len(cycles))
	for i, c := range cycles {
		fmt.Fprintf(w, "  %d: %s\n", i+1, strings.Join(c, " → "))
	}
}
