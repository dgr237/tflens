package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/token"
)

// emitJSON writes v to stdout as pretty-printed JSON. Exits on encoding
// failure (which should be impossible in practice with well-typed inputs).
func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatalf("encoding JSON: %v", err)
	}
}

// ---- JSON-shape structs ----
//
// These mirror the CLI's text-mode output but with explicit fields that are
// stable across releases. Consumers can rely on field names / shapes.

type jsonPosition struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func toJSONPos(p token.Position) jsonPosition {
	return jsonPosition{File: p.File, Line: p.Line, Column: p.Column}
}

type jsonEntity struct {
	ID   string       `json:"id"`
	Kind string       `json:"kind"`
	Type string       `json:"type,omitempty"` // non-empty for resource/data
	Name string       `json:"name"`
	Pos  jsonPosition `json:"pos"`
}

func toJSONEntity(e analysis.Entity) jsonEntity {
	return jsonEntity{
		ID:   e.ID(),
		Kind: string(e.Kind),
		Type: e.Type,
		Name: e.Name,
		Pos:  toJSONPos(e.Pos),
	}
}

type jsonValidationError struct {
	EntityID string       `json:"entity_id"`
	Ref      string       `json:"ref,omitempty"`
	Pos      jsonPosition `json:"pos"`
	Message  string       `json:"message"`
}

func toJSONValErr(e analysis.ValidationError) jsonValidationError {
	return jsonValidationError{
		EntityID: e.EntityID,
		Ref:      e.Ref,
		Pos:      toJSONPos(e.Pos),
		Message:  e.Error(),
	}
}

type jsonTypeError struct {
	EntityID string       `json:"entity_id"`
	Attr     string       `json:"attr"`
	Pos      jsonPosition `json:"pos"`
	Message  string       `json:"message"`
}

func toJSONTypeErr(e analysis.TypeCheckError) jsonTypeError {
	return jsonTypeError{
		EntityID: e.EntityID,
		Attr:     e.Attr,
		Pos:      toJSONPos(e.Pos),
		Message:  e.Msg,
	}
}

type jsonChange struct {
	Kind    string        `json:"kind"` // breaking | non-breaking | info
	Subject string        `json:"subject"`
	Detail  string        `json:"detail"`
	OldPos  *jsonPosition `json:"old_pos,omitempty"`
	NewPos  *jsonPosition `json:"new_pos,omitempty"`
}

func toJSONChange(c diff.Change) jsonChange {
	return jsonChange{
		Kind:    c.Kind.String(),
		Subject: c.Subject,
		Detail:  c.Detail,
		OldPos:  posPtr(c.OldPos),
		NewPos:  posPtr(c.NewPos),
	}
}

// posPtr returns nil when p is the zero Position (emitted for added-only or
// removed-only changes), and a pointer otherwise so `omitempty` takes effect.
func posPtr(p token.Position) *jsonPosition {
	if p.File == "" && p.Line == 0 && p.Column == 0 {
		return nil
	}
	jp := toJSONPos(p)
	return &jp
}

// ---- exit helper for JSON-mode commands ----

// exitJSON writes v and then exits with code. Used by validate/diff/whatif
// to signal findings via exit code while still emitting structured output.
func exitJSON(v any, code int) {
	emitJSON(v)
	if code != 0 {
		os.Exit(code)
	}
}

// Silence unused-import if a command stops using fmt.
var _ = fmt.Sprintln
