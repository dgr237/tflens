// Package constraint implements parsing and evaluation of Terraform-style
// module version constraints (e.g. "~> 1.2", ">= 1.0, < 2.0").
//
// Versions follow a minimal SemVer 2.0.0 model: MAJOR.MINOR.PATCH with an
// optional dotted prerelease suffix after '-'. Build metadata after '+' is
// accepted in input and discarded for comparison (SemVer §10).
//
// Prerelease matching is mathematical: 1.5.0-beta satisfies ">= 1.0.0".
// This differs from some package managers (notably npm) that require
// prereleases to be opted into — we can layer that on later if needed.
package constraint

import (
	"fmt"
	"strconv"
	"strings"
)

// V is a parsed semantic version.
type V struct {
	Major, Minor, Patch int
	// Prerelease is the raw dotted identifier list after '-', or empty.
	Prerelease string
}

// ParseVersion parses a version string. A leading "v" is permitted. Build
// metadata after '+' is stripped. Missing minor/patch default to 0 so
// "1" and "1.2" are valid inputs (the call site that cares about the
// original precision should use ParseConstraint, which records it).
func ParseVersion(s string) (V, error) {
	core, pre, err := splitVersion(s)
	if err != nil {
		return V{}, err
	}
	nums, err := splitCore(core)
	if err != nil {
		return V{}, err
	}
	return V{Major: nums[0], Minor: nums[1], Patch: nums[2], Prerelease: pre}, nil
}

func splitVersion(s string) (core, pre string, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return "", "", fmt.Errorf("empty version")
	}
	if before, _, found := strings.Cut(s, "+"); found {
		s = before // drop build metadata
	}
	if before, after, found := strings.Cut(s, "-"); found {
		return before, after, nil
	}
	return s, "", nil
}

func splitCore(core string) ([3]int, error) {
	parts := strings.Split(core, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return [3]int{}, fmt.Errorf("invalid version %q: expected 1-3 dotted components", core)
	}
	var nums [3]int
	for i, p := range parts {
		if p == "" {
			return [3]int{}, fmt.Errorf("invalid version %q: empty component", core)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, fmt.Errorf("invalid version %q: non-numeric component %q", core, p)
		}
		nums[i] = n
	}
	return nums, nil
}

func (v V) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	return s
}

// Compare returns -1, 0, 1 for a < b, a == b, a > b. Follows SemVer 2.0.0:
// a version with a prerelease is strictly less than the same core version
// without one; prerelease identifiers are compared per SemVer §11.
func Compare(a, b V) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	if a.Prerelease == b.Prerelease {
		return 0
	}
	if a.Prerelease == "" {
		return 1
	}
	if b.Prerelease == "" {
		return -1
	}
	return comparePrerelease(a.Prerelease, b.Prerelease)
}

// comparePrerelease implements SemVer 2.0.0 §11: dotted identifiers; purely
// numeric identifiers are compared numerically; alphanumeric are compared
// lexically; numeric identifiers have lower precedence than alphanumeric;
// a longer identifier list is greater than a prefix of itself.
func comparePrerelease(a, b string) int {
	aparts := strings.Split(a, ".")
	bparts := strings.Split(b, ".")
	n := min(len(aparts), len(bparts))
	for i := range n {
		ai, aIsNum := asInt(aparts[i])
		bi, bIsNum := asInt(bparts[i])
		switch {
		case aIsNum && bIsNum:
			if c := cmpInt(ai, bi); c != 0 {
				return c
			}
		case aIsNum:
			return -1
		case bIsNum:
			return 1
		default:
			if aparts[i] < bparts[i] {
				return -1
			}
			if aparts[i] > bparts[i] {
				return 1
			}
		}
	}
	return cmpInt(len(aparts), len(bparts))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func asInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}
