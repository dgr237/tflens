package constraint_test

import (
	"testing"

	"github.com/dgr237/tflens/pkg/constraint"
)

// mustParseC parses a constraint or fails the test.
func mustParseC(t *testing.T, s string) constraint.C {
	t.Helper()
	c, err := constraint.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return c
}

// mustV parses a version or fails the test.
func mustV(t *testing.T, s string) constraint.V {
	t.Helper()
	v, err := constraint.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestParseEmptyConstraintMatchesAny(t *testing.T) {
	c := mustParseC(t, "")
	for _, s := range []string{"0.0.1", "1.2.3", "999.0.0"} {
		if !c.Matches(mustV(t, s)) {
			t.Errorf("empty constraint should match %q", s)
		}
	}
}

func TestSimpleOperators(t *testing.T) {
	type matchCase struct {
		v     string
		match bool
	}
	cases := []struct {
		constraint string
		tests      []matchCase
	}{
		{"= 1.2.3", []matchCase{
			{"1.2.3", true},
			{"1.2.4", false},
			{"1.2.2", false},
		}},
		{"1.2.3", []matchCase{ // bare version = equality
			{"1.2.3", true},
			{"1.2.4", false},
		}},
		{"!= 1.2.3", []matchCase{
			{"1.2.3", false},
			{"1.2.4", true},
		}},
		{">= 1.2.3", []matchCase{
			{"1.2.2", false},
			{"1.2.3", true},
			{"1.2.4", true},
			{"2.0.0", true},
		}},
		{"> 1.2.3", []matchCase{
			{"1.2.3", false},
			{"1.2.4", true},
		}},
		{"<= 1.2.3", []matchCase{
			{"1.2.2", true},
			{"1.2.3", true},
			{"1.2.4", false},
		}},
		{"< 1.2.3", []matchCase{
			{"1.2.2", true},
			{"1.2.3", false},
		}},
	}
	for _, tc := range cases {
		c := mustParseC(t, tc.constraint)
		for _, mc := range tc.tests {
			if got := c.Matches(mustV(t, mc.v)); got != mc.match {
				t.Errorf("%q Matches(%q) = %v, want %v", tc.constraint, mc.v, got, mc.match)
			}
		}
	}
}

func TestPessimisticConstraint(t *testing.T) {
	// ~> 1.2   → >= 1.2.0, < 2.0.0
	c := mustParseC(t, "~> 1.2")
	ok := []string{"1.2.0", "1.2.9", "1.3.0", "1.99.99"}
	notOk := []string{"1.1.9", "2.0.0", "2.0.1"}
	for _, s := range ok {
		if !c.Matches(mustV(t, s)) {
			t.Errorf("~> 1.2 should match %q", s)
		}
	}
	for _, s := range notOk {
		if c.Matches(mustV(t, s)) {
			t.Errorf("~> 1.2 should NOT match %q", s)
		}
	}

	// ~> 1.2.3 → >= 1.2.3, < 1.3.0
	c = mustParseC(t, "~> 1.2.3")
	ok = []string{"1.2.3", "1.2.99"}
	notOk = []string{"1.2.2", "1.3.0", "2.0.0"}
	for _, s := range ok {
		if !c.Matches(mustV(t, s)) {
			t.Errorf("~> 1.2.3 should match %q", s)
		}
	}
	for _, s := range notOk {
		if c.Matches(mustV(t, s)) {
			t.Errorf("~> 1.2.3 should NOT match %q", s)
		}
	}
}

func TestPessimisticRejectsSingleComponent(t *testing.T) {
	if _, err := constraint.Parse("~> 1"); err == nil {
		t.Error("~> 1 should be rejected (precision < 2)")
	}
}

func TestIntersection(t *testing.T) {
	// ">= 1.0.0, < 2.0.0, != 1.5.0"
	c := mustParseC(t, ">= 1.0.0, < 2.0.0, != 1.5.0")
	tests := map[string]bool{
		"0.9.9": false,
		"1.0.0": true,
		"1.4.9": true,
		"1.5.0": false,
		"1.5.1": true,
		"2.0.0": false,
	}
	for s, want := range tests {
		if got := c.Matches(mustV(t, s)); got != want {
			t.Errorf("%q Matches(%q) = %v, want %v", c.String(), s, got, want)
		}
	}
}

func TestParseRejectsBadSyntax(t *testing.T) {
	bad := []string{
		">=",         // missing version
		">= abc",     // non-numeric
		", 1.0.0",    // empty clause
		">= 1.0.0,",  // trailing empty clause
	}
	for _, s := range bad {
		if _, err := constraint.Parse(s); err == nil {
			t.Errorf("Parse(%q) should fail", s)
		}
	}
}

func TestHighest(t *testing.T) {
	parse := func(s string) constraint.V { return mustV(t, s) }
	versions := []constraint.V{
		parse("1.0.0"), parse("1.2.0"), parse("1.2.5"),
		parse("1.3.0"), parse("2.0.0"),
	}

	c := mustParseC(t, "~> 1.2")
	got, ok := constraint.Highest(c, versions)
	if !ok || got.String() != "1.3.0" {
		t.Errorf("Highest(~> 1.2) = %v, %v; want 1.3.0", got, ok)
	}

	c = mustParseC(t, "~> 1.2.0")
	got, ok = constraint.Highest(c, versions)
	if !ok || got.String() != "1.2.5" {
		t.Errorf("Highest(~> 1.2.0) = %v, %v; want 1.2.5", got, ok)
	}

	c = mustParseC(t, ">= 3.0.0")
	if _, ok := constraint.Highest(c, versions); ok {
		t.Error("Highest(>= 3.0.0) should return no match")
	}
}

func TestHighestDoesNotMutateInput(t *testing.T) {
	versions := []constraint.V{
		mustV(t, "1.0.0"),
		mustV(t, "2.0.0"),
		mustV(t, "1.5.0"),
	}
	snapshot := append([]constraint.V(nil), versions...)
	_, _ = constraint.Highest(mustParseC(t, ">= 0.0.0"), versions)
	for i := range versions {
		if versions[i] != snapshot[i] {
			t.Errorf("Highest mutated input at index %d: got %v, want %v",
				i, versions[i], snapshot[i])
		}
	}
}

func TestPrereleaseMathematicalOrdering(t *testing.T) {
	// Documented behaviour: prereleases are matched mathematically, not via
	// npm-style opt-in. 1.5.0-beta should satisfy ">= 1.0.0".
	c := mustParseC(t, ">= 1.0.0")
	if !c.Matches(mustV(t, "1.5.0-beta")) {
		t.Error(">= 1.0.0 should match 1.5.0-beta (mathematical ordering)")
	}
	// And 1.0.0-beta should NOT satisfy ">= 1.0.0" (prerelease < release).
	if c.Matches(mustV(t, "1.0.0-beta")) {
		t.Error(">= 1.0.0 should NOT match 1.0.0-beta")
	}
}
