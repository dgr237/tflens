package constraint

import (
	"fmt"
	"sort"
	"strings"
)

// C is a parsed constraint expression: a conjunction ("," = AND) of simple
// rules like ">= 1.0" or "~> 1.2.3".
type C struct {
	rules []rule
	raw   string
}

type rule struct {
	op        string
	v         V
	precision int // dotted components in the source literal (for ~>)
}

// ops is ordered so longer prefixes come before shorter ones — "<=" must
// be tried before "<", etc.
var ops = []string{"~>", ">=", "<=", "!=", ">", "<", "="}

// Parse parses a Terraform-style constraint string. Whitespace around
// operators and commas is ignored. An empty string is an always-match
// constraint, consistent with Terraform treating a missing `version`
// attribute as "any version".
func Parse(s string) (C, error) {
	s = strings.TrimSpace(s)
	c := C{raw: s}
	if s == "" {
		return c, nil
	}
	for part := range strings.SplitSeq(s, ",") {
		r, err := parseRule(part)
		if err != nil {
			return C{}, err
		}
		c.rules = append(c.rules, r)
	}
	return c, nil
}

func parseRule(s string) (rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return rule{}, fmt.Errorf("empty constraint clause")
	}
	op := "="
	for _, candidate := range ops {
		if strings.HasPrefix(s, candidate) {
			op = candidate
			s = strings.TrimSpace(s[len(candidate):])
			break
		}
	}
	if s == "" {
		return rule{}, fmt.Errorf("missing version after operator %q", op)
	}
	precision := countCoreComponents(s)
	if op == "~>" && precision < 2 {
		return rule{}, fmt.Errorf("pessimistic constraint %q requires at least MAJOR.MINOR", op+" "+s)
	}
	v, err := ParseVersion(s)
	if err != nil {
		return rule{}, err
	}
	return rule{op: op, v: v, precision: precision}, nil
}

// countCoreComponents counts dotted components in the MAJOR[.MINOR[.PATCH]]
// portion of a version string, stripping the "v" prefix, prerelease, and
// build metadata first.
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

// Matches reports whether v satisfies every clause of the constraint.
func (c C) Matches(v V) bool {
	for _, r := range c.rules {
		if !r.matches(v) {
			return false
		}
	}
	return true
}

// String returns the original source text of the constraint.
func (c C) String() string { return c.raw }

func (r rule) matches(v V) bool {
	cmp := Compare(v, r.v)
	switch r.op {
	case "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "~>":
		if cmp < 0 {
			return false
		}
		return Compare(v, r.pessimisticUpperBound()) < 0
	}
	return false
}

// pessimisticUpperBound returns the exclusive upper bound implied by a
// "~>" rule. Concretely:
//
//	~> M.m      → < (M+1).0.0
//	~> M.m.p    → < M.(m+1).0
//
// Prerelease is cleared so the bound is a plain release.
func (r rule) pessimisticUpperBound() V {
	up := r.v
	up.Prerelease = ""
	switch r.precision {
	case 2:
		up.Major++
		up.Minor = 0
		up.Patch = 0
	case 3:
		up.Minor++
		up.Patch = 0
	}
	return up
}

// Highest returns the greatest version in versions that satisfies c, or
// (V{}, false) if none match. The input slice is not mutated.
func Highest(c C, versions []V) (V, bool) {
	sorted := append([]V(nil), versions...)
	sort.Slice(sorted, func(i, j int) bool {
		return Compare(sorted[i], sorted[j]) > 0 // descending
	})
	for _, v := range sorted {
		if c.Matches(v) {
			return v, true
		}
	}
	return V{}, false
}
