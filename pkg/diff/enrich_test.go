package diff_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/plan"
)

// fixturePath resolves a plan testdata fixture relative to the
// pkg/plan package — both packages share the same fixtures since
// the loader is what produces the *plan.Plan that EnrichFromPlan
// consumes.
func planFixture(t *testing.T, name string) *plan.Plan {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "plan", "testdata", name)
	p, err := plan.Load(path)
	if err != nil {
		t.Fatalf("load plan %s: %v", path, err)
	}
	return p
}

// TestEnrichFromPlanNilNoop pins the early-return contract: EnrichFromPlan
// with a nil plan returns the input changes verbatim, nothing added,
// nothing dropped. Lets cmd/diff treat the --enrich-with-plan flag
// as opt-in without branching on the boolean every time it calls
// the enricher.
func TestEnrichFromPlanNilNoop(t *testing.T) {
	in := []diff.Change{
		{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"},
	}
	out := diff.EnrichFromPlan(in, nil, nil)
	if len(out) != 1 || out[0].Subject != "var.x" {
		t.Errorf("nil plan should not modify changes; got %+v", out)
	}
}

// TestEnrichFromPlanUpdate covers the core happy-path: a plan with one
// update + one replace + one no-op produces the right number of
// plan-derived Change entries with the right Source tag, severity,
// and Subject formatting.
func TestEnrichFromPlanUpdate(t *testing.T) {
	p := planFixture(t, "update.json")
	out := diff.EnrichFromPlan(nil, p, nil)

	// Expectations from update.json:
	//   - vpc update (1 attr delta, non-force-new)             → 1 Informational
	//   - subnet replace (1 attr delta, force-new)             → 1 replace summary (Breaking)
	//                                                            + 1 attr-delta row (Breaking, ForceNew)
	//   - sg no-op                                             → 0 entries
	if got, want := len(out), 3; got != want {
		t.Fatalf("len(out) = %d, want %d; got %+v", got, want, out)
	}

	// All entries should be tagged as plan-derived.
	for _, c := range out {
		if c.Source != diff.SourcePlan {
			t.Errorf("Change %q has Source = %q, want %q",
				c.Subject, c.Source, diff.SourcePlan)
		}
	}

	// Find the per-attribute delta rows by Subject containing ":"
	// (the attr separator in attrDeltaChanges).
	var vpcAttr, subnetAttr *diff.Change
	for i := range out {
		if !strings.Contains(out[i].Subject, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(out[i].Subject, "aws_vpc.main"):
			vpcAttr = &out[i]
		case strings.HasPrefix(out[i].Subject, "aws_subnet.public"):
			subnetAttr = &out[i]
		}
	}
	if vpcAttr == nil {
		t.Fatal("missing vpc attribute delta")
	}
	if vpcAttr.Kind != diff.Informational {
		t.Errorf("vpc attr Kind = %v, want Informational", vpcAttr.Kind)
	}
	if subnetAttr == nil {
		t.Fatal("missing subnet attribute delta")
	}
	if subnetAttr.Kind != diff.Breaking {
		t.Errorf("subnet attr Kind = %v, want Breaking (force-new)", subnetAttr.Kind)
	}
	if subnetAttr.Hint == "" {
		t.Errorf("subnet attr should have a fix Hint (force-new triggers it)")
	}
}

// TestEnrichFromPlanCreateAndDelete confirms the create / delete
// summaries fire with the right severity. Replace is covered by
// TestEnrichFromPlanUpdate; this one covers the lifecycle bookends.
func TestEnrichFromPlanCreateAndDelete(t *testing.T) {
	p := planFixture(t, "nested_module.json")
	out := diff.EnrichFromPlan(nil, p, nil)

	// nested_module.json has:
	//   - module.network.aws_vpc.main update (1 attr delta)
	//   - module.network.module.subnets.aws_subnet.public[east] create (1 attr delta + 0 summary because
	//     create produces just the summary entry below)
	//   - data.aws_ami.latest read (filtered as no-op-equivalent... actually IsNoOp checks ["no-op"] only;
	//     ["read"] passes through ALL the predicate checks as false → not emitted)
	//
	// So we expect: 1 vpc update attr + 1 create summary = 2 entries.
	if got, want := len(out), 2; got != want {
		t.Fatalf("len(out) = %d, want %d; got %+v", got, want, out)
	}

	var createEntry *diff.Change
	for i := range out {
		if strings.Contains(out[i].Detail, "plan creates") {
			createEntry = &out[i]
		}
	}
	if createEntry == nil {
		t.Fatal("missing create summary entry")
	}
	if createEntry.Kind != diff.Informational {
		t.Errorf("create summary Kind = %v, want Informational", createEntry.Kind)
	}
}

// TestEnrichFromPlanPreservesExisting confirms enrichment doesn't
// drop or modify the static-side changes — they should appear in
// the output verbatim alongside the new plan-derived entries, and
// the merged list stays sorted by (Kind, Subject) so Breaking
// findings come first regardless of source.
func TestEnrichFromPlanPreservesExisting(t *testing.T) {
	staticChanges := []diff.Change{
		{Kind: diff.Breaking, Subject: "variable.required", Detail: "removed"},
		{Kind: diff.Informational, Subject: "out.docs", Detail: "description updated"},
	}
	p := planFixture(t, "update.json")
	out := diff.EnrichFromPlan(staticChanges, p, nil)

	// 2 static + 3 plan-derived = 5 total.
	if got, want := len(out), 5; got != want {
		t.Fatalf("len(out) = %d, want %d", got, want)
	}

	// First entry should be Breaking, last should be Informational —
	// the SliceStable sort is by (Kind, Subject), Breaking < others.
	if out[0].Kind != diff.Breaking {
		t.Errorf("out[0].Kind = %v, want Breaking", out[0].Kind)
	}
	if out[len(out)-1].Kind != diff.Informational {
		t.Errorf("out[-1].Kind = %v, want Informational", out[len(out)-1].Kind)
	}
}
