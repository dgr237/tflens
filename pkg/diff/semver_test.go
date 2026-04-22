package diff

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in    string
		major int
		minor int
		patch int
		parts int
	}{
		{"1", 1, 0, 0, 1},
		{"1.2", 1, 2, 0, 2},
		{"1.2.3", 1, 2, 3, 3},
		{"v1.2.3", 1, 2, 3, 3},
		{"1.2.3-alpha", 1, 2, 3, 3},
		{"1.2.3+build", 1, 2, 3, 3},
	}
	for _, c := range cases {
		v, n, err := parseSemver(c.in)
		if err != nil {
			t.Errorf("parseSemver(%q) error: %v", c.in, err)
			continue
		}
		if v.Major != c.major || v.Minor != c.minor || v.Patch != c.patch {
			t.Errorf("parseSemver(%q): got {%d,%d,%d}, want {%d,%d,%d}",
				c.in, v.Major, v.Minor, v.Patch, c.major, c.minor, c.patch)
		}
		if n != c.parts {
			t.Errorf("parseSemver(%q): parts=%d, want %d", c.in, n, c.parts)
		}
	}
}

func TestParseSemverErrors(t *testing.T) {
	for _, s := range []string{"", "abc", "1.2.3.4", "-1"} {
		if _, _, err := parseSemver(s); err == nil {
			t.Errorf("parseSemver(%q): expected error", s)
		}
	}
}

func TestSemverCompare(t *testing.T) {
	cases := []struct {
		a, b semver
		want int
	}{
		{semver{1, 0, 0}, semver{1, 0, 0}, 0},
		{semver{1, 0, 0}, semver{1, 0, 1}, -1},
		{semver{1, 0, 1}, semver{1, 0, 0}, 1},
		{semver{1, 2, 0}, semver{1, 3, 0}, -1},
		{semver{2, 0, 0}, semver{1, 99, 99}, 1},
	}
	for _, c := range cases {
		if got := c.a.compare(c.b); got != c.want {
			t.Errorf("%v.compare(%v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAtomToIntervals(t *testing.T) {
	// Spot-check a few important shapes.
	cases := []struct {
		atom     string
		loMajor  int
		hiMajor  int
		loClosed bool
		hiClosed bool
	}{
		{"= 1.2.3", 1, 1, true, true},
		{">= 1.0.0", 1, 0, true, false}, // hi is +∞ — we check only lo
		{">  1.0.0", 1, 0, false, false},
		{"< 2.0.0", 0, 2, false, false},
		{"~> 1.2", 1, 1, true, false}, // [1.2.0, 1.3.0) — major stays 1
	}
	for _, c := range cases {
		ivs, err := atomToIntervals(c.atom)
		if err != nil {
			t.Errorf("atomToIntervals(%q) error: %v", c.atom, err)
			continue
		}
		if len(ivs) != 1 {
			t.Errorf("atomToIntervals(%q): got %d intervals, want 1", c.atom, len(ivs))
			continue
		}
		iv := ivs[0]
		// For finite Lo, check major.
		if iv.Lo.Kind == 0 && iv.Lo.V.Major != c.loMajor {
			t.Errorf("atomToIntervals(%q): Lo.Major=%d, want %d", c.atom, iv.Lo.V.Major, c.loMajor)
		}
	}
}

func TestAtomNotEqualProducesTwoIntervals(t *testing.T) {
	ivs, err := atomToIntervals("!= 1.2.3")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(ivs) != 2 {
		t.Errorf("!= should produce 2 intervals, got %d", len(ivs))
	}
}

func TestPessimisticBounds(t *testing.T) {
	// ~> 1.2   → [1.2.0, 2.0.0)   (2 components: major stays, minor+patch can change)
	// ~> 1.2.3 → [1.2.3, 1.3.0)   (3 components: major+minor stay, patch can change)
	cases := []struct {
		in      string
		hiMajor int
		hiMinor int
	}{
		{"~> 1.2", 2, 0},
		{"~> 1.2.3", 1, 3},
	}
	for _, c := range cases {
		ivs, err := atomToIntervals(c.in)
		if err != nil {
			t.Fatalf("parse %q error: %v", c.in, err)
		}
		if len(ivs) != 1 || ivs[0].Hi.Kind != 0 {
			t.Errorf("%q: unexpected shape %+v", c.in, ivs)
			continue
		}
		hi := ivs[0].Hi.V
		if hi.Major != c.hiMajor || hi.Minor != c.hiMinor {
			t.Errorf("%q: hi=%v, want {%d.%d.0}", c.in, hi, c.hiMajor, c.hiMinor)
		}
	}

	// ~> 1 (1 component) is treated as >= 1.0.0 with no upper bound.
	ivs, err := atomToIntervals("~> 1")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(ivs) != 1 || ivs[0].Hi.Kind != 1 {
		t.Errorf("~> 1 should have +inf upper bound, got %+v", ivs)
	}
}

func TestCompareConstraintsEqual(t *testing.T) {
	if compareConstraints(">= 1.0", ">= 1.0") != relEqual {
		t.Error("identical constraints should be equal")
	}
}

func TestCompareConstraintsBroadened(t *testing.T) {
	// ">= 1.5" is a subset of ">= 1.0" — moving FROM the tighter to the looser is broadening.
	if r := compareConstraints(">= 1.5", ">= 1.0"); r != relBroadened {
		t.Errorf("expected relBroadened, got %v", r)
	}
}

func TestCompareConstraintsNarrowed(t *testing.T) {
	// ">= 1.0" → ">= 1.5": new is subset of old.
	if r := compareConstraints(">= 1.0", ">= 1.5"); r != relNarrowed {
		t.Errorf("expected relNarrowed, got %v", r)
	}
}

func TestCompareConstraintsDisjoint(t *testing.T) {
	// "~> 1.0" accepts [1.0.0, 2.0.0); "~> 2.0" accepts [2.0.0, 3.0.0) — no overlap.
	if r := compareConstraints("~> 1.0", "~> 2.0"); r != relDisjoint {
		t.Errorf("expected relDisjoint, got %v", r)
	}
}

func TestCompareConstraintsOverlap(t *testing.T) {
	// "~> 1.0" = [1.0, 2.0); ">= 1.5" = [1.5, ∞). Overlap on [1.5, 2.0). Neither subset.
	if r := compareConstraints("~> 1.0", ">= 1.5"); r != relOverlap {
		t.Errorf("expected relOverlap, got %v", r)
	}
}

func TestCompareConstraintsExactPinChange(t *testing.T) {
	// Exact pins with different versions are disjoint.
	if r := compareConstraints("1.0.0", "2.0.0"); r != relDisjoint {
		t.Errorf("expected relDisjoint for exact-pin change, got %v", r)
	}
}

func TestCompareConstraintsEmptyIsUniversal(t *testing.T) {
	// Empty string accepts everything; going from empty to a pinned version
	// is a narrowing.
	if r := compareConstraints("", "1.0.0"); r != relNarrowed {
		t.Errorf("expected relNarrowed for unpinned→pinned, got %v", r)
	}
	// And the reverse is broadening.
	if r := compareConstraints("1.0.0", ""); r != relBroadened {
		t.Errorf("expected relBroadened for pinned→unpinned, got %v", r)
	}
}

func TestCompareConstraintsCompound(t *testing.T) {
	// ">= 1.0, < 2.0" vs ">= 1.0, < 3.0" — new is strictly broader.
	if r := compareConstraints(">= 1.0, < 2.0", ">= 1.0, < 3.0"); r != relBroadened {
		t.Errorf("expected relBroadened for compound widening, got %v", r)
	}
}

func TestCompareConstraintsUnknownFallback(t *testing.T) {
	if r := compareConstraints("not-a-version", ">= 1.0"); r != relUnknown {
		t.Errorf("expected relUnknown for unparseable input, got %v", r)
	}
}

func TestClassifyVersionChangeUnparseableFallsBackToInformational(t *testing.T) {
	kind, detail := classifyVersionChange("provider version", "weird-format", ">= 1.0")
	if kind != Informational {
		t.Errorf("unparseable should fall back to Informational, got %v", kind)
	}
	if detail == "" {
		t.Error("detail should not be empty")
	}
}
