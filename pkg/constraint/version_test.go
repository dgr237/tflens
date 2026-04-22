package constraint_test

import (
	"testing"

	"github.com/dgr237/tflens/pkg/constraint"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want constraint.V
	}{
		{"1.2.3", constraint.V{Major: 1, Minor: 2, Patch: 3}},
		{"v1.2.3", constraint.V{Major: 1, Minor: 2, Patch: 3}},
		{"1.2", constraint.V{Major: 1, Minor: 2}},
		{"1", constraint.V{Major: 1}},
		{" 1.2.3 ", constraint.V{Major: 1, Minor: 2, Patch: 3}},
		{"1.2.3-alpha", constraint.V{Major: 1, Minor: 2, Patch: 3, Prerelease: "alpha"}},
		{"1.2.3-alpha.1", constraint.V{Major: 1, Minor: 2, Patch: 3, Prerelease: "alpha.1"}},
		{"1.2.3+build.5", constraint.V{Major: 1, Minor: 2, Patch: 3}},
		{"1.2.3-rc.1+build.5", constraint.V{Major: 1, Minor: 2, Patch: 3, Prerelease: "rc.1"}},
	}
	for _, tc := range cases {
		got, err := constraint.ParseVersion(tc.in)
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestParseVersionRejectsBadInputs(t *testing.T) {
	bad := []string{"", "abc", "1.2.3.4", "1..3", "-1.0.0", "1.a.0"}
	for _, in := range bad {
		if _, err := constraint.ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) = nil err, want error", in)
		}
	}
}

func TestCompareCore(t *testing.T) {
	v := func(s string) constraint.V {
		t.Helper()
		r, err := constraint.ParseVersion(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return r
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.0", "1.1.0", -1},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.2.3", "v1.2.3", 0},
	}
	for _, tc := range cases {
		if got := constraint.Compare(v(tc.a), v(tc.b)); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestComparePrereleasePerSemVer(t *testing.T) {
	// SemVer 2.0.0 §11: full precedence chain from the spec.
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	parse := func(s string) constraint.V {
		t.Helper()
		v, err := constraint.ParseVersion(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return v
	}
	for i := 0; i < len(ordered)-1; i++ {
		a, b := parse(ordered[i]), parse(ordered[i+1])
		if got := constraint.Compare(a, b); got != -1 {
			t.Errorf("Compare(%q, %q) = %d, want -1", ordered[i], ordered[i+1], got)
		}
		if got := constraint.Compare(b, a); got != 1 {
			t.Errorf("Compare(%q, %q) = %d, want 1", ordered[i+1], ordered[i], got)
		}
	}
}

func TestVersionString(t *testing.T) {
	cases := []struct {
		v    constraint.V
		want string
	}{
		{constraint.V{Major: 1, Minor: 2, Patch: 3}, "1.2.3"},
		{constraint.V{Major: 1, Minor: 2, Patch: 3, Prerelease: "rc.1"}, "1.2.3-rc.1"},
		{constraint.V{}, "0.0.0"},
	}
	for _, tc := range cases {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
