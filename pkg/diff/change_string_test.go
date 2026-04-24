package diff_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
)

func TestChangeStringIncludesKindSubjectDetail(t *testing.T) {
	c := diff.Change{Kind: diff.Breaking, Subject: "variable.x", Detail: "removed"}
	got := c.String()
	for _, want := range []string{"breaking", "variable.x", "removed"} {
		if !strings.Contains(got, want) {
			t.Errorf("Change.String() = %q; missing %q", got, want)
		}
	}
}
