package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
)

// parseSemver parses a version literal like "1", "1.2", or "1.2.3"
// (with optional leading "v" and optional prerelease/build metadata)
// and returns it along with the number of core components explicitly
// provided — needed by ~> to derive the implicit upper bound.
//
// Parsing delegates to hashicorp/go-version, the same library Terraform
// uses; we only count components ourselves because go-version always
// normalises to three segments internally (no way to tell "1.2" apart
// from "1.2.0" after parsing).
//
// Rejects literals with more than three core (MAJOR[.MINOR[.PATCH]])
// components. go-version itself accepts 4+ segments (Maven-style), but
// Terraform's constraint grammar — and our `~>` interval derivation
// — only defines behaviour for 1, 2, or 3.
func parseSemver(s string) (*version.Version, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, 0, fmt.Errorf("empty version")
	}
	n := countCoreComponents(s)
	if n > 3 {
		return nil, 0, fmt.Errorf("version %q has %d components; expected MAJOR[.MINOR[.PATCH]]", s, n)
	}
	v, err := version.NewVersion(s)
	if err != nil {
		return nil, 0, err
	}
	return v, n, nil
}

// countCoreComponents counts the dotted numeric segments in the core
// of a version literal — the part before any `-` (prerelease) or `+`
// (build metadata), after stripping any leading "v". Used to drive
// `~>` upper-bound derivation: `~> 1.2` must widen to `< 2.0.0`, while
// `~> 1.2.0` widens only to `< 1.3.0`.
func countCoreComponents(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if before, _, found := strings.Cut(s, "+"); found {
		s = before
	}
	if before, _, found := strings.Cut(s, "-"); found {
		s = before
	}
	return len(strings.Split(s, "."))
}

// mustVersion parses s or panics. Used to construct concrete versions
// for `~>` upper bounds from already-validated numeric segments — the
// input is always well-formed at call sites.
func mustVersion(s string) *version.Version {
	v, err := version.NewVersion(s)
	if err != nil {
		panic(fmt.Sprintf("mustVersion(%q): %v", s, err))
	}
	return v
}

// ---- interval model ----

// bound is an interval endpoint. Kind -1 means -∞, +1 means +∞, 0 means V.
type bound struct {
	Kind int
	V    *version.Version
}

var negInf = bound{Kind: -1}
var posInf = bound{Kind: 1}

func boundCmp(a, b bound) int {
	if a.Kind != b.Kind {
		if a.Kind < b.Kind {
			return -1
		}
		return 1
	}
	if a.Kind != 0 {
		return 0
	}
	return a.V.Compare(b.V)
}

// interval is [Lo, Hi] with LoClosed / HiClosed flags controlling inclusivity.
type interval struct {
	Lo       bound
	LoClosed bool
	Hi       bound
	HiClosed bool
}

// empty reports whether the interval contains no versions.
func (i interval) empty() bool {
	c := boundCmp(i.Lo, i.Hi)
	if c > 0 {
		return true
	}
	if c == 0 && !(i.LoClosed && i.HiClosed) {
		return true
	}
	return false
}

// contains reports whether i fully contains j.
func (i interval) contains(j interval) bool {
	// i.Lo must be <= j.Lo
	c := boundCmp(i.Lo, j.Lo)
	if c > 0 {
		return false
	}
	if c == 0 && !i.LoClosed && j.LoClosed {
		return false
	}
	// i.Hi must be >= j.Hi
	c = boundCmp(i.Hi, j.Hi)
	if c < 0 {
		return false
	}
	if c == 0 && !i.HiClosed && j.HiClosed {
		return false
	}
	return true
}

// intersect returns the intersection of a and b (may be empty).
func (a interval) intersect(b interval) interval {
	var r interval
	// Lo = max(a.Lo, b.Lo) with closedness = AND of closednesses at the max
	if c := boundCmp(a.Lo, b.Lo); c > 0 {
		r.Lo, r.LoClosed = a.Lo, a.LoClosed
	} else if c < 0 {
		r.Lo, r.LoClosed = b.Lo, b.LoClosed
	} else {
		r.Lo = a.Lo
		r.LoClosed = a.LoClosed && b.LoClosed
	}
	// Hi = min(a.Hi, b.Hi) with closedness = AND at the min
	if c := boundCmp(a.Hi, b.Hi); c < 0 {
		r.Hi, r.HiClosed = a.Hi, a.HiClosed
	} else if c > 0 {
		r.Hi, r.HiClosed = b.Hi, b.HiClosed
	} else {
		r.Hi = a.Hi
		r.HiClosed = a.HiClosed && b.HiClosed
	}
	return r
}

// mergeIntervals sorts and merges overlapping/touching intervals.
func mergeIntervals(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	sorted := make([]interval, len(in))
	copy(sorted, in)
	sort.Slice(sorted, func(i, j int) bool {
		c := boundCmp(sorted[i].Lo, sorted[j].Lo)
		if c != 0 {
			return c < 0
		}
		// Closed lower bound sorts before open one at same value.
		return sorted[i].LoClosed && !sorted[j].LoClosed
	})
	out := []interval{sorted[0]}
	for _, v := range sorted[1:] {
		last := &out[len(out)-1]
		// Overlap/touch test: last.Hi >= v.Lo with appropriate closedness
		c := boundCmp(last.Hi, v.Lo)
		touching := c > 0 || (c == 0 && (last.HiClosed || v.LoClosed))
		if !touching {
			out = append(out, v)
			continue
		}
		// Merge: extend last.Hi if v.Hi is further
		c2 := boundCmp(last.Hi, v.Hi)
		if c2 < 0 || (c2 == 0 && v.HiClosed && !last.HiClosed) {
			last.Hi = v.Hi
			last.HiClosed = v.HiClosed
		}
	}
	return out
}

// subsetOf reports whether every point in a is also in b.
// Assumes both are merged (sorted, disjoint).
func subsetOf(a, b []interval) bool {
	for _, ai := range a {
		covered := false
		for _, bi := range b {
			if bi.contains(ai) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// overlapsAny reports whether a and b share any point.
func overlapsAny(a, b []interval) bool {
	for _, ai := range a {
		for _, bi := range b {
			if !ai.intersect(bi).empty() {
				return true
			}
		}
	}
	return false
}

// ---- constraint parsing ----

// constraint is the set of semvers a Terraform constraint string admits.
type constraint struct {
	Intervals []interval
}

// parseConstraint parses a Terraform version constraint string like
// ">= 1.2.0, < 2.0.0" or "~> 4.0" into a constraint.
// An empty string accepts every version.
func parseConstraint(s string) (constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return constraint{Intervals: []interval{{Lo: negInf, Hi: posInf}}}, nil
	}
	atoms := strings.Split(s, ",")
	// Seed with the universal interval, then intersect each atom.
	result := []interval{{Lo: negInf, Hi: posInf}}
	for _, atom := range atoms {
		ivs, err := atomToIntervals(strings.TrimSpace(atom))
		if err != nil {
			return constraint{}, err
		}
		var next []interval
		for _, r := range result {
			for _, a := range ivs {
				i := r.intersect(a)
				if !i.empty() {
					next = append(next, i)
				}
			}
		}
		result = next
	}
	return constraint{Intervals: mergeIntervals(result)}, nil
}

// atomToIntervals converts one atom (e.g. ">= 1.2.0" or "~> 1.2") to its
// satisfying intervals. != produces two intervals; everything else produces one.
func atomToIntervals(atom string) ([]interval, error) {
	atom = strings.TrimSpace(atom)
	// Order matters: check longer operators first.
	var op string
	for _, o := range []string{"~>", ">=", "<=", "!=", "==", ">", "<", "="} {
		if strings.HasPrefix(atom, o) {
			op = o
			atom = strings.TrimSpace(atom[len(o):])
			break
		}
	}
	if op == "" {
		op = "=" // bare version is exact
	}
	v, nComponents, err := parseSemver(atom)
	if err != nil {
		return nil, err
	}
	switch op {
	case "=", "==":
		return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: bound{V: v}, HiClosed: true}}, nil
	case "!=":
		return []interval{
			{Lo: negInf, Hi: bound{V: v}, HiClosed: false},
			{Lo: bound{V: v}, LoClosed: false, Hi: posInf},
		}, nil
	case ">":
		return []interval{{Lo: bound{V: v}, LoClosed: false, Hi: posInf}}, nil
	case ">=":
		return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: posInf}}, nil
	case "<":
		return []interval{{Lo: negInf, Hi: bound{V: v}, HiClosed: false}}, nil
	case "<=":
		return []interval{{Lo: negInf, Hi: bound{V: v}, HiClosed: true}}, nil
	case "~>":
		// Pessimistic: rightmost component is allowed to increment.
		//   ~> X        → [X.0.0, ∞)       (1-component ~> is treated as >= X)
		//   ~> X.Y      → [X.Y.0, (X+1).0.0)  (minor can change within major)
		//   ~> X.Y.Z    → [X.Y.Z, X.(Y+1).0)  (patch can change within minor)
		seg := v.Segments()
		// Segments() pads to three: missing minor/patch are zero,
		// which is what we want here.
		for len(seg) < 3 {
			seg = append(seg, 0)
		}
		switch nComponents {
		case 1:
			return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: posInf}}, nil
		case 2:
			upper := mustVersion(fmt.Sprintf("%d.0.0", seg[0]+1))
			return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: bound{V: upper}, HiClosed: false}}, nil
		case 3:
			upper := mustVersion(fmt.Sprintf("%d.%d.0", seg[0], seg[1]+1))
			return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: bound{V: upper}, HiClosed: false}}, nil
		}
	}
	return nil, fmt.Errorf("unknown operator %q", op)
}

// ---- relation ----

type relation int

const (
	relUnknown   relation = iota // parse failure on either side
	relEqual                     // same satisfying set
	relBroadened                 // old ⊂ new (more versions accepted)
	relNarrowed                  // new ⊂ old (fewer versions accepted)
	relOverlap                   // neither subset of the other, but overlap exists
	relDisjoint                  // no overlap at all
)

// compareConstraints classifies how two constraint strings relate.
func compareConstraints(oldStr, newStr string) relation {
	a, err := parseConstraint(oldStr)
	if err != nil {
		return relUnknown
	}
	b, err := parseConstraint(newStr)
	if err != nil {
		return relUnknown
	}
	aInB := subsetOf(a.Intervals, b.Intervals)
	bInA := subsetOf(b.Intervals, a.Intervals)
	switch {
	case aInB && bInA:
		return relEqual
	case aInB:
		return relBroadened
	case bInA:
		return relNarrowed
	case overlapsAny(a.Intervals, b.Intervals):
		return relOverlap
	default:
		return relDisjoint
	}
}

// classifyVersionChange maps a version-constraint transition to a
// (ChangeKind, detail) pair. Returns (Informational, generic detail) when
// either constraint is unparseable.
func classifyVersionChange(label, oldV, newV string) (ChangeKind, string) {
	od, nd := displayVersion(oldV), displayVersion(newV)
	switch compareConstraints(oldV, newV) {
	case relEqual:
		return Informational, fmt.Sprintf("%s unchanged (%q)", label, od)
	case relBroadened:
		return NonBreaking, fmt.Sprintf("%s loosened: %q → %q (strictly more versions accepted)", label, od, nd)
	case relNarrowed:
		return Breaking, fmt.Sprintf("%s tightened: %q → %q (fewer versions accepted; some callers may be locked out)", label, od, nd)
	case relOverlap:
		return Breaking, fmt.Sprintf("%s partially narrowed: %q → %q (some previously-accepted versions now rejected)", label, od, nd)
	case relDisjoint:
		return Breaking, fmt.Sprintf("%s incompatible: %q → %q (no overlap with prior constraint)", label, od, nd)
	}
	// relUnknown — fall back
	return Informational, fmt.Sprintf("%s changed: %q → %q", label, od, nd)
}
