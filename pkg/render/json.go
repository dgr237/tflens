package render

import (
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/token"
)

// JSON-shape adapters for the CLI's --format=json mode.
//
// These mirror the text-mode output but with explicit fields whose
// names + tags are part of tflens's stable JSON contract. Consumers
// (CI scripts, dashboards, jq pipelines) parse them directly, so
// renaming a field or changing a tag is a major-version-breaking
// change. Add omitempty cautiously — that too is observable.
//
// Each Position / Entity / ValidationError / TypeError / Change has a
// JSON{...} struct + a `JSON{Kind}` constructor. Callers (the cmd
// package) compose them into command-specific envelopes.

// JSONPosition is the wire form of a token.Position.
type JSONPosition struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// jsonPos converts a token.Position into its wire form.
func jsonPos(p token.Position) JSONPosition {
	return JSONPosition{File: p.File, Line: p.Line, Column: p.Column}
}

// jsonPosPtr returns nil for a zero Position (the convention used for
// added-only / removed-only changes), and a pointer otherwise so an
// `omitempty` tag drops it from the output.
func jsonPosPtr(p token.Position) *JSONPosition {
	if p.File == "" && p.Line == 0 && p.Column == 0 {
		return nil
	}
	jp := jsonPos(p)
	return &jp
}

// JSONEntity is the wire form of an analysis.Entity.
type JSONEntity struct {
	ID   string       `json:"id"`
	Kind string       `json:"kind"`
	Type string       `json:"type,omitempty"` // non-empty for resource/data
	Name string       `json:"name"`
	Pos  JSONPosition `json:"pos"`
}

// jsonEnt converts an analysis.Entity into its wire form.
func jsonEnt(e analysis.Entity) JSONEntity {
	return JSONEntity{
		ID:   e.ID(),
		Kind: string(e.Kind),
		Type: e.Type,
		Name: e.Name,
		Pos:  jsonPos(e.Pos),
	}
}

// JSONValidationError is the wire form of an analysis.ValidationError.
type JSONValidationError struct {
	EntityID string       `json:"entity_id"`
	Ref      string       `json:"ref,omitempty"`
	Pos      JSONPosition `json:"pos"`
	Message  string       `json:"message"`
}

// jsonValErr converts an analysis.ValidationError into its wire form.
// Message is the formatted string from Error(), so consumers don't
// need to compose Pos + Ref themselves.
func jsonValErr(e analysis.ValidationError) JSONValidationError {
	return JSONValidationError{
		EntityID: e.EntityID,
		Ref:      e.Ref,
		Pos:      jsonPos(e.Pos),
		Message:  e.Error(),
	}
}

// JSONTypeError is the wire form of an analysis.TypeCheckError.
type JSONTypeError struct {
	EntityID string       `json:"entity_id"`
	Attr     string       `json:"attr"`
	Pos      JSONPosition `json:"pos"`
	Message  string       `json:"message"`
}

// jsonTypeErr converts an analysis.TypeCheckError into its wire form.
func jsonTypeErr(e analysis.TypeCheckError) JSONTypeError {
	return JSONTypeError{
		EntityID: e.EntityID,
		Attr:     e.Attr,
		Pos:      jsonPos(e.Pos),
		Message:  e.Msg,
	}
}

// JSONChange is the wire form of a diff.Change.
type JSONChange struct {
	Kind    string        `json:"kind"` // breaking | non-breaking | info
	Subject string        `json:"subject"`
	Detail  string        `json:"detail"`
	Hint    string        `json:"hint,omitempty"`
	OldPos  *JSONPosition `json:"old_pos,omitempty"`
	NewPos  *JSONPosition `json:"new_pos,omitempty"`
}

// jsonChg converts a diff.Change into its wire form. The Old/New
// position pointers are nil for changes that only have one side
// (additions, removals).
func jsonChg(c diff.Change) JSONChange {
	return JSONChange{
		Kind:    c.Kind.String(),
		Subject: c.Subject,
		Detail:  c.Detail,
		Hint:    c.Hint,
		OldPos:  jsonPosPtr(c.OldPos),
		NewPos:  jsonPosPtr(c.NewPos),
	}
}
