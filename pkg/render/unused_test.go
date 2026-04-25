package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/render"
)

func TestWriteUnusedEmpty(t *testing.T) {
	var b bytes.Buffer
	render.WriteUnused(&b, nil)
	if got := b.String(); got != "No unreferenced entities found.\n" {
		t.Errorf("empty unused = %q", got)
	}
}

func TestWriteUnusedListsEntities(t *testing.T) {
	var b bytes.Buffer
	render.WriteUnused(&b, []analysis.Entity{
		{Kind: analysis.KindVariable, Name: "orphan"},
		{Kind: analysis.KindLocal, Name: "stale"},
	})
	out := b.String()
	if !strings.HasPrefix(out, "Unreferenced entities (2):\n") {
		t.Errorf("missing header; got:\n%s", out)
	}
	if !strings.Contains(out, "  variable.orphan\n") {
		t.Errorf("missing variable.orphan; got:\n%s", out)
	}
	if !strings.Contains(out, "  local.stale\n") {
		t.Errorf("missing local.stale; got:\n%s", out)
	}
}
