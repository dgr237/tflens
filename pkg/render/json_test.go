package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/token"
)

func TestJSONPosCarriesAllFields(t *testing.T) {
	got := render.JSONPos(token.Position{File: "main.tf", Line: 42, Column: 9})
	want := render.JSONPosition{File: "main.tf", Line: 42, Column: 9}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestJSONPosPtrZeroIsNil pins down the convention used by JSONChg:
// a zero Position (no file, no line, no column) round-trips as nil
// rather than {file:"", line:0, column:0}, so the omitempty tag on
// JSONChange.OldPos / NewPos drops the field entirely.
func TestJSONPosPtrZeroIsNil(t *testing.T) {
	if render.JSONPosPtr(token.Position{}) != nil {
		t.Error("zero position should yield nil pointer")
	}
}

func TestJSONPosPtrNonZeroIsPopulated(t *testing.T) {
	got := render.JSONPosPtr(token.Position{File: "x.tf", Line: 1})
	if got == nil {
		t.Fatal("non-zero position should yield non-nil pointer")
	}
	if got.File != "x.tf" || got.Line != 1 {
		t.Errorf("got %+v, want {File:x.tf, Line:1}", *got)
	}
}

func TestJSONEntPropagatesFields(t *testing.T) {
	got := render.JSONEnt(analysis.Entity{
		Kind: analysis.KindResource, Type: "aws_vpc", Name: "main",
		Pos: token.Position{File: "f.tf", Line: 5, Column: 1},
	})
	if got.ID != "resource.aws_vpc.main" {
		t.Errorf("ID = %q, want resource.aws_vpc.main", got.ID)
	}
	if got.Kind != "resource" || got.Type != "aws_vpc" || got.Name != "main" {
		t.Errorf("Kind/Type/Name: %+v", got)
	}
	if got.Pos.File != "f.tf" || got.Pos.Line != 5 {
		t.Errorf("Pos = %+v", got.Pos)
	}
}

// TestJSONEntDataKindOmitsTypeWhenSet is a sanity check that the
// Type field is preserved for resource/data entities (omitempty
// only kicks in for non-resource/data, where Type is empty).
func TestJSONEntDataKind(t *testing.T) {
	got := render.JSONEnt(analysis.Entity{
		Kind: analysis.KindData, Type: "aws_caller_identity", Name: "current",
	})
	b, _ := json.Marshal(got)
	s := string(b)
	for _, want := range []string{
		`"kind":"data"`,
		`"type":"aws_caller_identity"`,
		`"name":"current"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in JSON %s", want, s)
		}
	}
}

func TestJSONValErrUsesFormattedMessage(t *testing.T) {
	got := render.JSONValErr(analysis.ValidationError{
		EntityID: "module.x",
		Ref:      "variable.y",
		Pos:      token.Position{File: "main.tf", Line: 7, Column: 3},
	})
	if got.EntityID != "module.x" || got.Ref != "variable.y" {
		t.Errorf("EntityID/Ref: %+v", got)
	}
	// Default Error() uses Pos + Ref + EntityID — message should NOT
	// be empty.
	if got.Message == "" {
		t.Error("Message should be the formatted Error() string, not empty")
	}
}

func TestJSONTypeErrPropagatesFields(t *testing.T) {
	got := render.JSONTypeErr(analysis.TypeCheckError{
		EntityID: "variable.x",
		Attr:     "default",
		Pos:      token.Position{File: "f.tf", Line: 3},
		Msg:      "default is not a string",
	})
	if got.EntityID != "variable.x" || got.Attr != "default" || got.Message != "default is not a string" {
		t.Errorf("got %+v", got)
	}
}

func TestJSONChgIncludesOptionalFields(t *testing.T) {
	got := render.JSONChg(diff.Change{
		Kind:    diff.Breaking,
		Subject: "variable.x",
		Detail:  "removed",
		Hint:    "callers will fail",
		OldPos:  token.Position{File: "old.tf", Line: 1},
		NewPos:  token.Position{File: "new.tf", Line: 2},
	})
	if got.Kind != "breaking" {
		t.Errorf("Kind = %q, want breaking", got.Kind)
	}
	if got.OldPos == nil || got.OldPos.File != "old.tf" {
		t.Errorf("OldPos = %+v", got.OldPos)
	}
	if got.NewPos == nil || got.NewPos.File != "new.tf" {
		t.Errorf("NewPos = %+v", got.NewPos)
	}
	if got.Hint != "callers will fail" {
		t.Errorf("Hint = %q", got.Hint)
	}
}

// TestJSONChgZeroPositionsOmittedFromMarshalled covers the omitempty
// contract: a Change with no NewPos (purely a removal) doesn't
// emit "new_pos":null in the JSON output.
func TestJSONChgZeroPositionsOmittedFromMarshalled(t *testing.T) {
	c := render.JSONChg(diff.Change{
		Kind: diff.Breaking, Subject: "x", Detail: "removed",
		OldPos: token.Position{File: "old.tf", Line: 1},
		// NewPos zero
	})
	b, _ := json.Marshal(c)
	s := string(b)
	if strings.Contains(s, "new_pos") {
		t.Errorf("new_pos should be omitted for zero-value position; got: %s", s)
	}
	if !strings.Contains(s, "old_pos") {
		t.Errorf("old_pos should be present when populated; got: %s", s)
	}
}

// TestJSONChgKindStrings pins down the wire labels — these are part
// of the public CLI JSON contract.
func TestJSONChgKindStrings(t *testing.T) {
	cases := map[diff.ChangeKind]string{
		diff.Breaking:      "breaking",
		diff.NonBreaking:   "non-breaking",
		diff.Informational: "info",
	}
	for k, want := range cases {
		got := render.JSONChg(diff.Change{Kind: k}).Kind
		if got != want {
			t.Errorf("Kind %v → %q, want %q", k, got, want)
		}
	}
}
