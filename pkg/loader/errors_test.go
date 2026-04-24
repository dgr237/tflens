package loader_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/token"
)

func TestParseErrorErrorWithPosition(t *testing.T) {
	pe := loader.ParseError{
		Pos: token.Position{File: "main.tf", Line: 10, Column: 4},
		Msg: "unclosed block",
	}
	got := pe.Error()
	if !strings.Contains(got, "main.tf:10:4") || !strings.Contains(got, "unclosed block") {
		t.Errorf("got %q", got)
	}
}

func TestParseErrorErrorWithoutPosition(t *testing.T) {
	pe := loader.ParseError{Msg: "freestanding message"}
	if got := pe.Error(); got != "freestanding message" {
		t.Errorf("got %q, want %q", got, "freestanding message")
	}
}

func TestFileErrorAggregatesPerFile(t *testing.T) {
	fe := loader.FileError{
		Path: "main.tf",
		Errors: []loader.ParseError{
			{Pos: token.Position{File: "main.tf", Line: 1}, Msg: "first"},
			{Pos: token.Position{File: "main.tf", Line: 5}, Msg: "second"},
		},
	}
	got := fe.Error()
	for _, want := range []string{"main.tf", "first", "second"} {
		if !strings.Contains(got, want) {
			t.Errorf("FileError.Error() = %q; missing %q", got, want)
		}
	}
}
