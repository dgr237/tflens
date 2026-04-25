package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/analysis"
)

// writeValidate emits the validate subcommand's text result. With
// no errors of any kind, writes the "no errors" baseline; otherwise
// emits up to three sections (undefined references, cross-module
// issues, type errors) separated by blank lines. Cmd-side picks
// stdout vs stderr depending on whether anything was flagged.
func writeValidate(
	w io.Writer,
	refErrs, crossErrs []analysis.ValidationError,
	typeErrs []analysis.TypeCheckError,
) {
	if len(refErrs)+len(crossErrs)+len(typeErrs) == 0 {
		fmt.Fprintln(w, "No validation errors found.")
		return
	}
	sep := writeValidationSection(w, false, "Undefined references", refErrs)
	sep = writeValidationSection(w, sep, "Cross-module issues", crossErrs)
	if len(typeErrs) > 0 {
		if sep {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Type errors (%d):\n", len(typeErrs))
		for _, e := range typeErrs {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
}

// writeValidationSection writes the title + count header and then one
// indented line per error. The bool sep tracks whether a previous
// section already wrote — when true the next non-empty section gets a
// leading blank line. Returns the new sep value.
func writeValidationSection(
	w io.Writer, sep bool, title string, items []analysis.ValidationError,
) bool {
	if len(items) == 0 {
		return sep
	}
	if sep {
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%s (%d):\n", title, len(items))
	for _, e := range items {
		fmt.Fprintf(w, "  %s\n", e)
	}
	return true
}
