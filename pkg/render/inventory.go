package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/analysis"
)

// inventorySections lists the entity kinds in display order plus the
// human-readable section heading each one gets in writeInventory.
// Order matches the legacy cmd output so JSON-vs-text consumers see
// the same shape.
var inventorySections = []struct {
	kind  analysis.EntityKind
	title string
}{
	{analysis.KindVariable, "Variables"},
	{analysis.KindLocal, "Locals"},
	{analysis.KindData, "Data sources"},
	{analysis.KindResource, "Resources"},
	{analysis.KindModule, "Modules"},
	{analysis.KindOutput, "Outputs"},
}

// writeInventory emits the inventory subcommand's text result: an
// "Entities: N" header followed by one section per entity kind that
// has any entries. Each entity line shows the canonical ID padded to
// 40 chars and, when present, a "(file:line)" location suffix.
func writeInventory(w io.Writer, m *analysis.Module) {
	fmt.Fprintf(w, "Entities: %d\n", len(m.Entities()))
	for _, s := range inventorySections {
		entities := m.Filter(s.kind)
		if len(entities) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d):\n", s.title, len(entities))
		for _, e := range entities {
			if loc := e.Location(); loc != "" {
				fmt.Fprintf(w, "  %-40s  (%s)\n", e.ID(), loc)
			} else {
				fmt.Fprintf(w, "  %s\n", e.ID())
			}
		}
	}
}
