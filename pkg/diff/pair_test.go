package diff_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

func TestExitCodeFor(t *testing.T) {
	cases := map[int]int{
		0: 0,
		1: 1,
		5: 1,
	}
	for in, want := range cases {
		if got := diff.ExitCodeFor(in); got != want {
			t.Errorf("ExitCodeFor(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestHintForCrossValidateMsg(t *testing.T) {
	// Each case asserts a substring fragment of the cross_validate
	// message produces a non-empty hint with a recognisable phrase.
	cases := []struct {
		msg          string
		hintContains string
	}{
		{"module.x passes unknown argument \"foo\"", "remove the argument"},
		{"module.x does not pass required input \"foo\"", "add the input"},
		{"module.x passes \"foo\" as string but child variable expects number", "convert the value"},
		{"module.x references module.x.foo but the child module declares no such output", "restore the output"},
		{"some unrelated error nobody will ever see", ""},
	}
	for _, c := range cases {
		got := diff.HintForCrossValidateMsg(c.msg)
		if c.hintContains == "" {
			if got != "" {
				t.Errorf("HintForCrossValidateMsg(%q) = %q, want empty", c.msg, got)
			}
			continue
		}
		if !strings.Contains(got, c.hintContains) {
			t.Errorf("HintForCrossValidateMsg(%q) = %q; want substring %q",
				c.msg, got, c.hintContains)
		}
	}
}

// TestPairResultInteresting covers the three Status branches:
// added/removed are always interesting, changed is interesting only
// when content or attrs moved.
func TestPairResultInteresting(t *testing.T) {
	cases := []struct {
		name string
		r    diff.PairResult
		want bool
	}{
		{
			name: "added is always interesting",
			r:    diff.PairResult{Pair: loader.ModuleCallPair{Status: loader.StatusAdded}},
			want: true,
		},
		{
			name: "removed is always interesting",
			r:    diff.PairResult{Pair: loader.ModuleCallPair{Status: loader.StatusRemoved}},
			want: true,
		},
		{
			name: "changed with no content + no attr move is NOT interesting",
			r: diff.PairResult{Pair: loader.ModuleCallPair{
				Status:    loader.StatusChanged,
				OldSource: "x", NewSource: "x",
				OldVersion: "1", NewVersion: "1",
			}},
			want: false,
		},
		{
			name: "changed with content changes is interesting",
			r: diff.PairResult{
				Pair:    loader.ModuleCallPair{Status: loader.StatusChanged},
				Changes: []diff.Change{{Kind: diff.Breaking, Detail: "x"}},
			},
			want: true,
		},
		{
			name: "changed with attr move is interesting",
			r: diff.PairResult{Pair: loader.ModuleCallPair{
				Status:    loader.StatusChanged,
				OldSource: "x", NewSource: "y",
			}},
			want: true,
		},
	}
	for _, c := range cases {
		if got := c.r.Interesting(); got != c.want {
			t.Errorf("%s: Interesting() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPairResultAttrsChanged(t *testing.T) {
	cases := []struct {
		name string
		p    loader.ModuleCallPair
		want bool
	}{
		{"all empty", loader.ModuleCallPair{}, false},
		{"identical source + version", loader.ModuleCallPair{
			OldSource: "x", NewSource: "x", OldVersion: "1", NewVersion: "1",
		}, false},
		{"source changed", loader.ModuleCallPair{
			OldSource: "x", NewSource: "y",
		}, true},
		{"version changed", loader.ModuleCallPair{
			OldVersion: "1", NewVersion: "2",
		}, true},
		{"both changed", loader.ModuleCallPair{
			OldSource: "x", NewSource: "y", OldVersion: "1", NewVersion: "2",
		}, true},
	}
	for _, c := range cases {
		got := diff.PairResult{Pair: c.p}.AttrsChanged()
		if got != c.want {
			t.Errorf("%s: AttrsChanged() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPairResultHasContentChanges(t *testing.T) {
	if (diff.PairResult{}).HasContentChanges() {
		t.Error("zero PairResult should have no content changes")
	}
	if !(diff.PairResult{Changes: []diff.Change{{}}}).HasContentChanges() {
		t.Error("PairResult with one change should have content changes")
	}
}

// TestConsumptionChangesForLocalNoParent: the function is nil-safe
// when the pair has no NewParent (e.g. an added top-level call with
// no caller).
func TestConsumptionChangesForLocalNoParent(t *testing.T) {
	got := diff.ConsumptionChangesForLocal(loader.ModuleCallPair{
		Status: loader.StatusChanged,
	})
	if got != nil {
		t.Errorf("expected nil for missing NewParent, got %v", got)
	}
}
