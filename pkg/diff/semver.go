package diff

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH version. Prerelease and build metadata
// are stripped during parsing — they complicate ordering disproportionately
// to their real-world frequency in Terraform module/provider constraints.
type semver struct {
	Major, Minor, Patch int
}

func (a semver) compare(b semver) int {
	if a.Major != b.Major {
		if a.Major < b.Major {
			return -1
		}
		return 1
	}
	if a.Minor != b.Minor {
		if a.Minor < b.Minor {
			return -1
		}
		return 1
	}
	if a.Patch != b.Patch {
		if a.Patch < b.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// parseSemver parses "X", "X.Y", or "X.Y.Z", returning the semver and the
// number of components explicitly provided (needed by ~> for the implied
// upper bound).
func parseSemver(s string) (semver, int, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		s = s[:idx]
	}
	if s == "" {
		return semver{}, 0, fmt.Errorf("empty version")
	}
	parts := strings.Split(s, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return semver{}, 0, fmt.Errorf("bad semver %q", s)
	}
	v := semver{}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return semver{}, 0, fmt.Errorf("bad semver %q: %v", s, err)
		}
		if n < 0 {
			return semver{}, 0, fmt.Errorf("negative component in %q", s)
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		case 2:
			v.Patch = n
		}
	}
	return v, len(parts), nil
}

// ---- interval model ----

// bound is an interval endpoint. Kind -1 means -∞, +1 means +∞, 0 means V.
type bound struct {
	Kind int
	V    semver
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
	return a.V.compare(b.V)
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
		switch nComponents {
		case 1:
			return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: posInf}}, nil
		case 2:
			upper := semver{Major: v.Major + 1}
			return []interval{{Lo: bound{V: v}, LoClosed: true, Hi: bound{V: upper}, HiClosed: false}}, nil
		case 3:
			upper := semver{Major: v.Major, Minor: v.Minor + 1}
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
