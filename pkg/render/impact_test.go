package render_test

import (
	"bytes"
	"strings"
	"testing"
)

func TestRendererImpactEmpty(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Impact("variable.x", nil)
	want := "No entities are affected by changes to variable.x\n"
	if got := b.String(); got != want {
		t.Errorf("empty impact = %q", got)
	}
}

func TestRendererImpactSingularEntityIsGrammar(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Impact("variable.x", []string{"local.y"})
	if !strings.Contains(b.String(), "1 entity is affected") {
		t.Errorf("singular grammar wrong; got:\n%s", b.String())
	}
}

func TestRendererImpactPluralEntitiesAreGrammar(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Impact("variable.x", []string{"local.a", "local.b", "local.c"})
	out := b.String()
	if !strings.Contains(out, "3 entities are affected") {
		t.Errorf("plural grammar wrong; got:\n%s", out)
	}
	for _, id := range []string{"local.a", "local.b", "local.c"} {
		if !strings.Contains(out, "  "+id+"\n") {
			t.Errorf("missing %q in output:\n%s", id, out)
		}
	}
}
