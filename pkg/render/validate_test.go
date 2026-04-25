package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/render"
)

func TestWriteValidateNoErrors(t *testing.T) {
	var b bytes.Buffer
	render.WriteValidate(&b, nil, nil, nil)
	if got := b.String(); got != "No validation errors found.\n" {
		t.Errorf("no errors = %q", got)
	}
}

func TestWriteValidateAllThreeSections(t *testing.T) {
	refErrs := []analysis.ValidationError{{EntityID: "x", Ref: "var.a", Msg: "var.a missing"}}
	crossErrs := []analysis.ValidationError{{EntityID: "module.kid", Ref: "var.b", Msg: "module.kid missing input b"}}
	typeErrs := []analysis.TypeCheckError{{EntityID: "variable.c", Msg: "default not convertible"}}

	var b bytes.Buffer
	render.WriteValidate(&b, refErrs, crossErrs, typeErrs)
	out := b.String()
	for _, want := range []string{
		"Undefined references (1):",
		"Cross-module issues (1):",
		"Type errors (1):",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q; got:\n%s", want, out)
		}
	}
}

func TestWriteValidateOnlyTypeErrorsNoLeadingBlank(t *testing.T) {
	// When only type errors are present, the section should not be
	// preceded by a blank line.
	var b bytes.Buffer
	render.WriteValidate(&b, nil, nil, []analysis.TypeCheckError{{EntityID: "v", Msg: "x"}})
	if strings.HasPrefix(b.String(), "\n") {
		t.Errorf("unexpected leading blank line; got %q", b.String())
	}
}

func TestWriteValidateBlanksBetweenSections(t *testing.T) {
	refErrs := []analysis.ValidationError{{EntityID: "x", Ref: "var.a", Msg: "var.a missing"}}
	crossErrs := []analysis.ValidationError{{EntityID: "y", Ref: "var.b", Msg: "y missing input b"}}
	var b bytes.Buffer
	render.WriteValidate(&b, refErrs, crossErrs, nil)
	out := b.String()
	// Expect exactly one blank line between the two sections.
	if !strings.Contains(out, "\n\nCross-module issues") {
		t.Errorf("expected blank line before Cross-module section; got:\n%s", out)
	}
}
