package analysis_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/token"
)

// TestEntityStringIsCanonicalID confirms Entity.String() returns the
// same value as Entity.ID() — that's the contract callers rely on
// (Entity is used directly in formatted output via %s).
func TestEntityStringIsCanonicalID(t *testing.T) {
	cases := []analysis.Entity{
		{Kind: analysis.KindResource, Type: "aws_vpc", Name: "main"},
		{Kind: analysis.KindData, Type: "aws_caller_identity", Name: "current"},
		{Kind: analysis.KindVariable, Name: "env"},
		{Kind: analysis.KindLocal, Name: "region"},
		{Kind: analysis.KindOutput, Name: "vpc_id"},
		{Kind: analysis.KindModule, Name: "vpc"},
	}
	for _, e := range cases {
		if e.String() != e.ID() {
			t.Errorf("%+v: String() = %q, ID() = %q — should match", e, e.String(), e.ID())
		}
	}
}

func TestValidationErrorErrorWithMsg(t *testing.T) {
	e := analysis.ValidationError{
		EntityID: "module.x",
		Pos:      token.Position{File: "main.tf", Line: 7, Column: 3},
		Msg:      "module.x does not pass required input \"y\"",
	}
	got := e.Error()
	if !strings.Contains(got, "main.tf:7:3") {
		t.Errorf("error should include position; got %q", got)
	}
	if !strings.Contains(got, "required input") {
		t.Errorf("error should include the Msg; got %q", got)
	}
}

func TestValidationErrorErrorDefaultFormat(t *testing.T) {
	// When Msg is empty, Error() falls back to the "<ref> is undefined" template.
	e := analysis.ValidationError{
		EntityID: "resource.aws_vpc.main",
		Ref:      "variable.gone",
		Pos:      token.Position{File: "main.tf", Line: 1, Column: 1},
	}
	got := e.Error()
	if !strings.Contains(got, "variable.gone is undefined") {
		t.Errorf("default format should mention undefined ref; got %q", got)
	}
	if !strings.Contains(got, "resource.aws_vpc.main") {
		t.Errorf("default format should mention the referencing entity; got %q", got)
	}
}

func TestTFTypeString(t *testing.T) {
	cases := map[string]*analysis.TFType{
		"unknown":      nil,
		"string":       {Kind: analysis.TypeString},
		"number":       {Kind: analysis.TypeNumber},
		"bool":         {Kind: analysis.TypeBool},
		"null":         {Kind: analysis.TypeNull},
		"any":          {Kind: analysis.TypeAny},
		"list(any)":    {Kind: analysis.TypeList},
		"list(string)": {Kind: analysis.TypeList, Elem: &analysis.TFType{Kind: analysis.TypeString}},
		"set(any)":     {Kind: analysis.TypeSet},
		"map(any)":     {Kind: analysis.TypeMap},
	}
	for want, in := range cases {
		if got := in.String(); got != want {
			t.Errorf("%+v: String() = %q, want %q", in, got, want)
		}
	}
}

func TestTypeCheckErrorErrorIncludesPosAndMsg(t *testing.T) {
	e := analysis.TypeCheckError{
		EntityID: "variable.x",
		Attr:     "default",
		Pos:      token.Position{File: "f.tf", Line: 5, Column: 2},
		Msg:      "default is not a string",
	}
	got := e.Error()
	if !strings.Contains(got, "f.tf:5:2") || !strings.Contains(got, "default is not a string") {
		t.Errorf("got %q", got)
	}
}
