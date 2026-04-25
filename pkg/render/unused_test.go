package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

func TestRendererUnusedEmpty(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Unused(nil)
	if got := b.String(); got != "No unreferenced entities found.\n" {
		t.Errorf("empty unused = %q", got)
	}
}

func TestRendererUnusedListsEntities(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Unused([]analysis.Entity{
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
