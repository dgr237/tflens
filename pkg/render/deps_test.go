package render_test

import (
	"bytes"
	"strings"
	"testing"
)

func TestRendererDepsBothPopulated(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Deps("resource.aws_vpc.main",
		[]string{"variable.cidr"},
		[]string{"resource.aws_subnet.public"})
	want := "Entity:  resource.aws_vpc.main\n" +
		"\nDepends on (1):\n" +
		"  variable.cidr\n" +
		"\nReferenced by (1):\n" +
		"  resource.aws_subnet.public\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererDepsEmptySectionsRenderNone(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).Deps("variable.x", nil, nil)
	out := b.String()
	if !strings.Contains(out, "Depends on (0):\n  (none)\n") {
		t.Errorf("missing '(none)' for empty Depends on; got:\n%s", out)
	}
	if !strings.Contains(out, "Referenced by (0):\n  (none)\n") {
		t.Errorf("missing '(none)' for empty Referenced by; got:\n%s", out)
	}
}
