package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/token"
)

// These tests assert the wire-format shape of the JSON envelopes
// produced by the renderer. They drive through the public JSONRenderer
// methods (Diff, Validate, Inventory, Unused) and unmarshal the
// captured bytes into the public envelope types — no direct calls to
// the (now-private) per-field adapter helpers like jsonChg / jsonEnt.

// TestJSONChangePopulatesPositionsAndHint exercises diff.Change →
// JSONChange shape via Diff: the Hint surfaces, and OldPos / NewPos
// become populated *JSONPosition pointers when the source ranges
// are non-zero.
func TestJSONChangePopulatesPositionsAndHint(t *testing.T) {
	out := renderDiffJSON(t, "main", ".", nil, []diff.Change{{
		Kind:    diff.Breaking,
		Subject: "variable.x",
		Detail:  "removed",
		Hint:    "callers will fail",
		OldPos:  token.Position{File: "old.tf", Line: 1, Column: 1},
		NewPos:  token.Position{File: "new.tf", Line: 2, Column: 1},
	}})
	if len(out.RootChanges) != 1 {
		t.Fatalf("RootChanges len = %d, want 1", len(out.RootChanges))
	}
	c := out.RootChanges[0]
	if c.Kind != "breaking" {
		t.Errorf("Kind = %q, want breaking", c.Kind)
	}
	if c.Hint != "callers will fail" {
		t.Errorf("Hint = %q", c.Hint)
	}
	if c.OldPos == nil || c.OldPos.File != "old.tf" || c.OldPos.Line != 1 {
		t.Errorf("OldPos = %+v, want populated old.tf:1", c.OldPos)
	}
	if c.NewPos == nil || c.NewPos.File != "new.tf" || c.NewPos.Line != 2 {
		t.Errorf("NewPos = %+v, want populated new.tf:2", c.NewPos)
	}
}

// TestJSONChangeOmitsZeroPositions covers the omitempty contract:
// a change with no NewPos (purely a removal) emits no "new_pos" key
// — the field is dropped entirely from the marshalled JSON.
func TestJSONChangeOmitsZeroPositions(t *testing.T) {
	var b bytes.Buffer
	jsonRenderer(&b).Diff("main", ".", nil, []diff.Change{{
		Kind: diff.Breaking, Subject: "x", Detail: "removed",
		OldPos: token.Position{File: "old.tf", Line: 1, Column: 1},
		// NewPos zero
	}})
	s := b.String()
	if strings.Contains(s, "new_pos") {
		t.Errorf("new_pos should be omitted for zero-value position; got: %s", s)
	}
	if !strings.Contains(s, "old_pos") {
		t.Errorf("old_pos should be present when populated; got: %s", s)
	}
}

// TestJSONChangeKindLabels: the wire labels for ChangeKind are part
// of the public CLI JSON contract. Pin them per kind via Diff.
func TestJSONChangeKindLabels(t *testing.T) {
	cases := map[diff.ChangeKind]string{
		diff.Breaking:      "breaking",
		diff.NonBreaking:   "non-breaking",
		diff.Informational: "info",
	}
	for k, want := range cases {
		out := renderDiffJSON(t, "main", ".", nil, []diff.Change{{Kind: k, Subject: "x"}})
		if len(out.RootChanges) != 1 {
			t.Fatalf("kind %v: RootChanges len = %d", k, len(out.RootChanges))
		}
		if got := out.RootChanges[0].Kind; got != want {
			t.Errorf("kind %v → %q, want %q", k, got, want)
		}
	}
}

// TestJSONEntityShape covers the analysis.Entity → JSONEntity
// adapter via Inventory. Resource entities emit the canonical
// "resource.type.name" ID plus separate kind/type/name/pos fields.
func TestJSONEntityShape(t *testing.T) {
	mod := inventoryFromSrc(t, `resource "aws_vpc" "main" {}`)
	var b bytes.Buffer
	jsonRenderer(&b).Inventory(mod)

	var out struct {
		Total    int                 `json:"total"`
		Entities []render.JSONEntity `json:"entities"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, b.String())
	}
	if len(out.Entities) != 1 {
		t.Fatalf("Entities len = %d, want 1", len(out.Entities))
	}
	e := out.Entities[0]
	if e.ID != "resource.aws_vpc.main" {
		t.Errorf("ID = %q", e.ID)
	}
	if e.Kind != "resource" || e.Type != "aws_vpc" || e.Name != "main" {
		t.Errorf("Kind/Type/Name: %+v", e)
	}
}

// TestJSONEntityDataKindOmitsTypeWhenAbsent: data sources keep their
// Type field; non-resource/data kinds (variable, local, output) emit
// no "type" key thanks to omitempty. Verified by inspecting raw JSON.
func TestJSONEntityDataKindWireFormat(t *testing.T) {
	mod := inventoryFromSrc(t, `data "aws_caller_identity" "current" {}`)
	var b bytes.Buffer
	jsonRenderer(&b).Inventory(mod)
	s := b.String()
	for _, want := range []string{
		`"kind": "data"`,
		`"type": "aws_caller_identity"`,
		`"name": "current"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in JSON %s", want, s)
		}
	}
}

// TestJSONValidationErrorPopulatesMessage exercises the
// analysis.ValidationError → JSONValidationError adapter via the
// Validate renderer. The message field is the formatted Error()
// string (Pos + Ref + EntityID), not empty.
func TestJSONValidationErrorPopulatesMessage(t *testing.T) {
	var b bytes.Buffer
	jsonRenderer(&b).Validate(
		[]analysis.ValidationError{{
			EntityID: "module.x",
			Ref:      "variable.y",
			Pos:      token.Position{File: "main.tf", Line: 7, Column: 3},
		}},
		nil, nil,
	)
	var out struct {
		UndefinedReferences []render.JSONValidationError `json:"undefined_references"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.UndefinedReferences) != 1 {
		t.Fatalf("UndefinedReferences len = %d", len(out.UndefinedReferences))
	}
	v := out.UndefinedReferences[0]
	if v.EntityID != "module.x" || v.Ref != "variable.y" {
		t.Errorf("EntityID/Ref: %+v", v)
	}
	if v.Message == "" {
		t.Error("Message should be the formatted Error() string, not empty")
	}
}

// TestJSONTypeErrorPopulatesFields exercises the
// analysis.TypeCheckError → JSONTypeError adapter via Validate.
func TestJSONTypeErrorPopulatesFields(t *testing.T) {
	var b bytes.Buffer
	jsonRenderer(&b).Validate(nil, nil, []analysis.TypeCheckError{{
		EntityID: "variable.x",
		Attr:     "default",
		Pos:      token.Position{File: "f.tf", Line: 3},
		Msg:      "default is not a string",
	}})
	var out struct {
		TypeErrors []render.JSONTypeError `json:"type_errors"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.TypeErrors) != 1 {
		t.Fatalf("TypeErrors len = %d", len(out.TypeErrors))
	}
	te := out.TypeErrors[0]
	if te.EntityID != "variable.x" || te.Attr != "default" || te.Message != "default is not a string" {
		t.Errorf("got %+v", te)
	}
}
