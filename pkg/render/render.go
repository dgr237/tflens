// Package render emits human-readable text for diff results. The
// functions are io.Writer-based so they're trivially testable with a
// bytes.Buffer, and so cmd/ can compose them with whatever surrounding
// formatting it wants without each subcommand re-deriving the
// "Breaking → Non-breaking → Informational" section pattern.
package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/diff"
)

// writeChange emits a single change line:
//
//	<indent><Subject>: <Detail>
//
// followed by, when c.Hint is non-empty:
//
//	<indent>  hint: <Hint>
//
// The two-space hint indent is hard-coded — keeping it constant
// across the codebase is the whole point of this helper.
func writeChange(w io.Writer, indent string, c diff.Change) {
	// Plan-derived findings get a "[plan]" prefix so reviewers can
	// tell at a glance which findings came from the static analyser
	// vs the terraform plan. Source==SourceStatic / "" → no prefix
	// (the historical default; existing emitters don't set the field).
	prefix := ""
	if c.Source == diff.SourcePlan {
		prefix = "[plan] "
	}
	fmt.Fprintf(w, "%s%s%s: %s\n", indent, prefix, c.Subject, c.Detail)
	if c.Hint != "" {
		fmt.Fprintf(w, "%s  hint: %s\n", indent, c.Hint)
	}
}

// bucketByKind partitions changes into Breaking, NonBreaking, and
// Informational lists, preserving the input order within each bucket.
// Changes whose Kind doesn't match any of those three are silently
// dropped — there are no other kinds today.
func bucketByKind(changes []diff.Change) (breaking, nonBreaking, info []diff.Change) {
	for _, c := range changes {
		switch c.Kind {
		case diff.Breaking:
			breaking = append(breaking, c)
		case diff.NonBreaking:
			nonBreaking = append(nonBreaking, c)
		case diff.Informational:
			info = append(info, c)
		}
	}
	return breaking, nonBreaking, info
}

// writeChangesByKind emits each non-empty bucket of changes under a
// "<headingIndent><Kind label> (<count>):" heading, with each change
// rendered at lineIndent via writeChange.
//
// Headings use the canonical labels "Breaking", "Non-breaking",
// "Informational" — kept consistent across all subcommands so output
// is greppable and reviewers learn one vocabulary.
//
// Empty buckets are skipped entirely (no heading, no spacing). When
// changes is empty the function writes nothing.
func writeChangesByKind(w io.Writer, headingIndent, lineIndent string, changes []diff.Change) {
	breaking, nonBreaking, info := bucketByKind(changes)
	writeBucket(w, headingIndent, lineIndent, "Breaking", breaking)
	writeBucket(w, headingIndent, lineIndent, "Non-breaking", nonBreaking)
	writeBucket(w, headingIndent, lineIndent, "Informational", info)
}

func writeBucket(w io.Writer, headingIndent, lineIndent, label string, list []diff.Change) {
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "%s%s (%d):\n", headingIndent, label, len(list))
	for _, c := range list {
		writeChange(w, lineIndent, c)
	}
}
