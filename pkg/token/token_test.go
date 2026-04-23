package token_test

import (
	"testing"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/token"
)

func TestPositionString(t *testing.T) {
	cases := []struct {
		name string
		pos  token.Position
		want string
	}{
		{"with_file", token.Position{File: "main.tf", Line: 12, Column: 7}, "main.tf:12:7"},
		{"no_file", token.Position{Line: 3, Column: 1}, "3:1"},
		{"zero_value", token.Position{}, "0:0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pos.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromRange(t *testing.T) {
	r := hcl.Range{
		Filename: "main.tf",
		Start:    hcl.Pos{Line: 5, Column: 11},
		End:      hcl.Pos{Line: 5, Column: 20},
	}
	got := token.FromRange(r)
	want := token.Position{File: "main.tf", Line: 5, Column: 11}
	if got != want {
		t.Errorf("FromRange = %+v, want %+v", got, want)
	}
}

func TestFromRangeZeroIsZeroPosition(t *testing.T) {
	if got := token.FromRange(hcl.Range{}); got != (token.Position{}) {
		t.Errorf("FromRange(zero) = %+v, want zero Position", got)
	}
}

func TestFromPos(t *testing.T) {
	got := token.FromPos("schema.tf", hcl.Pos{Line: 9, Column: 4})
	want := token.Position{File: "schema.tf", Line: 9, Column: 4}
	if got != want {
		t.Errorf("FromPos = %+v, want %+v", got, want)
	}
}
