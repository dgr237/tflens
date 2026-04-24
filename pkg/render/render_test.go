package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/render"
)

func TestWriteChangeNoHint(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChange(&buf, "  ", diff.Change{
		Kind: diff.Breaking, Subject: "variable.x", Detail: "removed",
	})
	got := buf.String()
	want := "  variable.x: removed\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteChangeWithHint(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChange(&buf, "    ", diff.Change{
		Kind:    diff.Breaking,
		Subject: "variable.x",
		Detail:  "removed",
		Hint:    "callers passing this variable will fail",
	})
	got := buf.String()
	want := "    variable.x: removed\n      hint: callers passing this variable will fail\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBucketByKindPreservesOrderWithinBucket(t *testing.T) {
	in := []diff.Change{
		{Kind: diff.Breaking, Subject: "a"},
		{Kind: diff.Informational, Subject: "b"},
		{Kind: diff.Breaking, Subject: "c"},
		{Kind: diff.NonBreaking, Subject: "d"},
		{Kind: diff.Informational, Subject: "e"},
	}
	br, nb, info := render.BucketByKind(in)

	check := func(name string, got []diff.Change, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: got %d entries, want %d", name, len(got), len(want))
			return
		}
		for i, w := range want {
			if got[i].Subject != w {
				t.Errorf("%s[%d]: got subject %q, want %q", name, i, got[i].Subject, w)
			}
		}
	}
	check("breaking", br, "a", "c")
	check("nonBreaking", nb, "d")
	check("info", info, "b", "e")
}

func TestBucketByKindEmptyInput(t *testing.T) {
	br, nb, info := render.BucketByKind(nil)
	if br != nil || nb != nil || info != nil {
		t.Errorf("nil input should produce nil buckets, got %v / %v / %v", br, nb, info)
	}
}

func TestWriteChangesByKindOrderingAndHeadings(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChangesByKind(&buf, "  ", "    ", []diff.Change{
		{Kind: diff.NonBreaking, Subject: "out.added", Detail: "added"},
		{Kind: diff.Breaking, Subject: "var.x", Detail: "removed", Hint: "fix"},
		{Kind: diff.Informational, Subject: "out.docs", Detail: "doc"},
		{Kind: diff.Breaking, Subject: "res.r", Detail: "renamed"},
	})
	got := buf.String()
	want := "" +
		"  Breaking (2):\n" +
		"    var.x: removed\n" +
		"      hint: fix\n" +
		"    res.r: renamed\n" +
		"  Non-breaking (1):\n" +
		"    out.added: added\n" +
		"  Informational (1):\n" +
		"    out.docs: doc\n"
	if got != want {
		t.Errorf("got:\n%q\n\nwant:\n%q", got, want)
	}
}

func TestWriteChangesByKindSkipsEmptyBuckets(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChangesByKind(&buf, "  ", "    ", []diff.Change{
		{Kind: diff.Informational, Subject: "x", Detail: "y"},
	})
	got := buf.String()
	if strings.Contains(got, "Breaking") || strings.Contains(got, "Non-breaking") {
		t.Errorf("should not emit empty Breaking / Non-breaking sections; got:\n%s", got)
	}
	if !strings.Contains(got, "Informational (1):") {
		t.Errorf("missing Informational heading; got:\n%s", got)
	}
}

func TestWriteChangesByKindEmptyInputWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChangesByKind(&buf, "  ", "    ", nil)
	if buf.Len() != 0 {
		t.Errorf("empty input should write nothing, got %q", buf.String())
	}
}

// TestWriteChangesByKindHonorsArbitraryIndents covers cmd/whatif's
// deeper-indent use case: heading at "    ", lines at "      ".
func TestWriteChangesByKindHonorsArbitraryIndents(t *testing.T) {
	var buf bytes.Buffer
	render.WriteChangesByKind(&buf, "    ", "      ", []diff.Change{
		{Kind: diff.Breaking, Subject: "x", Detail: "y"},
	})
	got := buf.String()
	want := "    Breaking (1):\n      x: y\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
