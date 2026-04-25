package render

import (
	"fmt"
	"io"
)

// WriteImpact emits the impact subcommand's text result: a header line
// summarising the count and an indented list of every transitively-
// affected entity ID. An empty `affected` writes the "nothing affected"
// baseline.
func WriteImpact(w io.Writer, id string, affected []string) {
	if len(affected) == 0 {
		fmt.Fprintf(w, "No entities are affected by changes to %s\n", id)
		return
	}
	fmt.Fprintf(w, "If %s changes, %d %s affected (in evaluation order):\n",
		id, len(affected), entityIsAre(len(affected)))
	for _, aid := range affected {
		fmt.Fprintf(w, "  %s\n", aid)
	}
}

// entityIsAre returns "entity is" / "entities are" depending on n —
// keeps the WriteImpact header grammatically correct for both
// singular and plural cases.
func entityIsAre(n int) string {
	if n == 1 {
		return "entity is"
	}
	return "entities are"
}
