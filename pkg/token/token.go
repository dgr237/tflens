// Package token defines the source-position type used throughout tflens.
//
// Position is a thin wrapper over the start of a hcl.Range — kept as its
// own type so callers don't need to import hcl just to print or compare a
// location.
package token

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// Position describes a source location.
type Position struct {
	File   string
	Line   int
	Column int
}

func (p Position) String() string {
	if p.File != "" {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// FromRange builds a Position from the start of a hcl.Range.
func FromRange(r hcl.Range) Position {
	return Position{File: r.Filename, Line: r.Start.Line, Column: r.Start.Column}
}

// FromPos builds a Position from a hcl.Pos plus a filename. Useful when
// you have the position separate from a range (e.g. from a diagnostic).
func FromPos(filename string, p hcl.Pos) Position {
	return Position{File: filename, Line: p.Line, Column: p.Column}
}
