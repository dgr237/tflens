package render_test

import (
	"bytes"
	"testing"

	"github.com/dgr237/tflens/pkg/render"
)

func TestWriteCyclesEmpty(t *testing.T) {
	var b bytes.Buffer
	render.WriteCycles(&b, nil)
	if got := b.String(); got != "No cycles detected.\n" {
		t.Errorf("empty cycles = %q", got)
	}
}

func TestWriteCyclesNumberedList(t *testing.T) {
	var b bytes.Buffer
	render.WriteCycles(&b, [][]string{
		{"resource.a", "resource.b", "resource.a"},
		{"local.x", "local.y", "local.x"},
	})
	want := "Cycles detected (2):\n" +
		"  1: resource.a → resource.b → resource.a\n" +
		"  2: local.x → local.y → local.x\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
