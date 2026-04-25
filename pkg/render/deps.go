package render

import (
	"fmt"
	"io"
)

// writeDeps emits the deps subcommand's text result: the entity ID
// followed by its direct dependencies and dependents, each section
// counted and rendered with a "(none)" placeholder when empty.
func writeDeps(w io.Writer, id string, deps, dependents []string) {
	fmt.Fprintf(w, "Entity:  %s\n", id)
	writeIDList(w, "\nDepends on", deps)
	writeIDList(w, "\nReferenced by", dependents)
}

// writeIDList prints a "<heading> (N):" line followed by either an
// indented "(none)" or one line per id. Shared by writeDeps for the
// two symmetric sections.
func writeIDList(w io.Writer, heading string, ids []string) {
	fmt.Fprintf(w, "%s (%d):\n", heading, len(ids))
	if len(ids) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, id := range ids {
		fmt.Fprintf(w, "  %s\n", id)
	}
}
