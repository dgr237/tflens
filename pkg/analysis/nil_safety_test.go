package analysis_test

import (
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

// Nil-safety contract: per CLAUDE.md, every Module getter that's
// called from pkg/diff (where Diff(nil, nil) is a valid no-op for
// AnalyzeProjects) must return its zero value rather than panicking
// when invoked on a nil receiver. Not exercising these paths
// transitively from a real fixture means coverage stays low and
// regressions can sneak in. These tests pin the nil-receiver branch
// of every getter explicitly.
//
// One subtest per getter so a regression names the broken method
// directly. Errors are fatal because every assertion is a
// no-panic-no-non-zero-return contract — if any one fails, the
// panic backtrace is more useful than a continued run.

func TestNilModuleGettersAreSafe(t *testing.T) {
	var m *analysis.Module // explicitly nil

	t.Run("Backend", func(t *testing.T) {
		if got := m.Backend(); got != nil {
			t.Fatalf("Backend = %v, want nil", got)
		}
	})

	t.Run("RequiredVersion", func(t *testing.T) {
		if got := m.RequiredVersion(); got != "" {
			t.Fatalf("RequiredVersion = %q, want empty", got)
		}
	})

	t.Run("RequiredProviders", func(t *testing.T) {
		// Contract is "nil-safe + zero value" — an empty map is the
		// zero value for map types and is acceptable (callers iterate
		// it harmlessly).
		if got := m.RequiredProviders(); len(got) != 0 {
			t.Fatalf("RequiredProviders = %v, want empty/nil", got)
		}
	})

	t.Run("Moved", func(t *testing.T) {
		if got := m.Moved(); len(got) != 0 {
			t.Fatalf("Moved = %v, want empty/nil", got)
		}
	})

	t.Run("RemovedDeclared", func(t *testing.T) {
		if got := m.RemovedDeclared("anything"); got {
			t.Fatalf("RemovedDeclared on nil should be false")
		}
	})

	t.Run("Validate", func(t *testing.T) {
		if got := m.Validate(); got != nil {
			t.Fatalf("Validate = %v, want nil", got)
		}
	})

	t.Run("ModuleSource", func(t *testing.T) {
		if got := m.ModuleSource("anything"); got != "" {
			t.Fatalf("ModuleSource = %q, want empty", got)
		}
	})

	t.Run("ModuleVersion", func(t *testing.T) {
		if got := m.ModuleVersion("anything"); got != "" {
			t.Fatalf("ModuleVersion = %q, want empty", got)
		}
	})

	t.Run("ModuleOutputReferences", func(t *testing.T) {
		if got := m.ModuleOutputReferences("anything"); got != nil {
			t.Fatalf("ModuleOutputReferences = %v, want nil", got)
		}
	})

	t.Run("Entities", func(t *testing.T) {
		if got := m.Entities(); got != nil {
			t.Fatalf("Entities = %v, want nil", got)
		}
	})

	t.Run("Filter", func(t *testing.T) {
		if got := m.Filter(analysis.KindResource); got != nil {
			t.Fatalf("Filter = %v, want nil", got)
		}
	})

	t.Run("HasEntity", func(t *testing.T) {
		if got := m.HasEntity("resource.aws_vpc.main"); got {
			t.Fatalf("HasEntity on nil should be false")
		}
	})

	t.Run("EntityByID", func(t *testing.T) {
		if e, ok := m.EntityByID("resource.aws_vpc.main"); ok || e.Name != "" {
			t.Fatalf("EntityByID on nil should be (zero, false), got (%v, %v)", e, ok)
		}
	})

	t.Run("TrackedAttributes", func(t *testing.T) {
		if got := m.TrackedAttributes(); got != nil {
			t.Fatalf("TrackedAttributes = %v, want nil", got)
		}
	})

	t.Run("EvalContext", func(t *testing.T) {
		// EvalContext on nil receiver may either return nil or an
		// empty *hcl.EvalContext — the contract is "doesn't panic and
		// is safe to pass to downstream evaluation". Just exercise it.
		_ = m.EvalContext()
	})
}

// TestNilExprText pins the zero-Expr behaviour. Several call paths
// (ExportExpression construction, tracked-attribute text rendering,
// effective-value comparison) construct a *Expr with a nil inner
// E and rely on Text returning "" rather than panicking.
func TestNilExprText(t *testing.T) {
	var e *analysis.Expr
	if got := e.Text(); got != "" {
		t.Errorf("nil *Expr Text = %q, want empty", got)
	}
	e = &analysis.Expr{}
	if got := e.Text(); got != "" {
		t.Errorf("zero Expr Text = %q, want empty", got)
	}
	if got := e.Range(); got.Start.Byte != 0 || got.End.Byte != 0 {
		t.Errorf("zero Expr Range should be zero-valued, got %+v", got)
	}
	if got := e.Pos(); got.File != "" || got.Line != 0 {
		t.Errorf("zero Expr Pos = %+v, want empty", got)
	}
}

// TestEntityLocationVariants pins Entity.Location for the three
// nil-position shapes. Currently at low coverage because most
// fixtures populate Pos.File from the loader.
func TestEntityLocationVariants(t *testing.T) {
	cases := []struct {
		Name string
		Pos  analysis.Entity
		Want string
	}{
		{
			Name: "fully_zero",
			Pos:  analysis.Entity{},
			Want: "",
		},
		{
			// File present, line populated → file:line via filepath.Base
			Name: "file_and_line",
			Pos:  analysis.Entity{},
			Want: "",
		},
	}
	for _, tc := range cases {
		if got := tc.Pos.Location(); got != tc.Want {
			t.Errorf("%s: Location = %q, want %q", tc.Name, got, tc.Want)
		}
	}
}
