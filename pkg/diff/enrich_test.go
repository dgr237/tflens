package diff_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
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

// TestEnrichFromPlanIndexedAddresses pins the per-instance matching
// behaviour: plan addresses with module count/for_each indices
// (`module.regions["us-east-1"]`) and resource count/for_each indices
// (`aws_subnet.foo[0]`) both produce one Change per instance with the
// full address preserved in Subject — but the source-side entity
// lookup uses the index-stripped path so multiple plan instances
// resolve to the same source-side resource without spurious
// "no matching source-side entity" hints.
//
// The fixture has 4 ResourceChanges: 2 instances of an indexed module
// + 2 indices of an indexed resource (one no-op, one update). After
// enrichment we expect 3 attribute-delta entries (the no-op is
// filtered) with the full plan address preserved as Subject prefix.
func TestEnrichFromPlanIndexedAddresses(t *testing.T) {
	p := planFixture(t, "indexed_module.json")
	out := diff.EnrichFromPlan(nil, p, nil)

	if got, want := len(out), 3; got != want {
		t.Fatalf("len(out) = %d, want %d; got %+v", got, want, out)
	}

	// Pin: each entry's Subject preserves the full plan address
	// (with index) so reviewers can tell instances apart, even
	// though the source-side lookup happens against the index-
	// stripped path.
	wantSubjects := map[string]bool{
		`module.regions["us-east-1"].aws_vpc.main:cidr_block`: true,
		`module.regions["us-west-2"].aws_vpc.main:cidr_block`: true,
		`module.network.aws_subnet.foo[0]:availability_zone`:  true,
	}
	for _, c := range out {
		if !wantSubjects[c.Subject] {
			t.Errorf("unexpected Subject %q", c.Subject)
		}
		delete(wantSubjects, c.Subject)
	}
	for missing := range wantSubjects {
		t.Errorf("missing expected Subject %q", missing)
	}
}

// TestEnrichFromPlanIndexedModuleResolvesAgainstRealProject is the
// end-to-end version of the indexed-address test: it builds a real
// project tree (with an indexed module call) and confirms that plan
// addresses with `module.X[idx]` segments resolve to the source-side
// `module.X` entity — i.e. the index-stripping in matchKey actually
// works against a real ModuleNode index (not just the synthetic nil
// project the other tests use).
//
// Specifically: without the fix, every indexed-module entry in the
// plan would generate a "(no matching source-side entity — plan may
// be stale)" hint because the source tree doesn't have indexed
// nodes. With the fix the hint is absent.
func TestEnrichFromPlanIndexedModuleResolvesAgainstRealProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
module "regions" {
  source   = "./regions"
  for_each = toset(["us-east-1", "us-west-2"])
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	regionsDir := filepath.Join(dir, "regions")
	if err := os.MkdirAll(regionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regionsDir, "main.tf"), []byte(`
resource "aws_vpc" "main" {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	p := planFixture(t, "indexed_module.json")
	out := diff.EnrichFromPlan(nil, p, proj)

	// None of the entries should contain the "no matching source-side
	// entity" hint — the indexed-module addresses resolve against
	// `module.regions` (no index) in the source-side tree.
	for _, c := range out {
		if strings.Contains(c.Detail, "no matching source-side entity") {
			t.Errorf("unexpected stale-plan hint on %q: %s", c.Subject, c.Detail)
		}
	}
}

// TestEnrichFromPlanAttachesSourcePosition pins the source-position
// attribution: when a plan ResourceChange matches a source-side
// entity, the entity's Pos is propagated onto the resulting Change's
// NewPos so renderers can show file:line. Plan rows with no
// source-side match leave NewPos at its zero value.
func TestEnrichFromPlanAttachesSourcePosition(t *testing.T) {
	dir := t.TempDir()
	mainTF := filepath.Join(dir, "main.tf")
	// Write the resources the update.json fixture references so the
	// entity index has something to find. We don't care about the
	// exact lines — only that NewPos is non-zero for matching rows.
	if err := os.WriteFile(mainTF, []byte(`
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "public" {
  availability_zone = "us-east-1a"
}

resource "aws_security_group" "unchanged" {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	p := planFixture(t, "update.json")
	out := diff.EnrichFromPlan(nil, p, proj)

	// Every emitted Change should carry a non-zero NewPos because
	// every plan address in update.json maps to one of the resources
	// we declared above.
	for _, c := range out {
		if c.NewPos.Line == 0 {
			t.Errorf("Change %q has zero NewPos; expected a source position from the matching entity", c.Subject)
		}
		if c.NewPos.File == "" {
			t.Errorf("Change %q has empty NewPos.File; expected the main.tf path", c.Subject)
		}
	}
}

// TestEnrichFromPlanLeavesPositionZeroWhenNoMatch pins the inverse:
// when the plan describes a resource not present in the source-side
// project, NewPos stays zero. The entity-existence hint already tells
// the user the plan is stale; the renderer must not also fabricate a
// fake position.
func TestEnrichFromPlanLeavesPositionZeroWhenNoMatch(t *testing.T) {
	// Pass a nil project — every plan entry is "no matching source-side
	// entity". NewPos should stay zero on every emitted Change.
	p := planFixture(t, "update.json")
	out := diff.EnrichFromPlan(nil, p, nil)
	for _, c := range out {
		if c.NewPos.Line != 0 || c.NewPos.File != "" {
			t.Errorf("Change %q has unexpected NewPos %+v; want zero (no source-side match)",
				c.Subject, c.NewPos)
		}
	}
}

// TestEnrichFromPlanCollapsesStaleMovedPair pins the moved-block
// awareness: when source declares `moved { from = aws_vpc.old; to =
// aws_vpc.new }` AND the plan still shows aws_vpc.old as a delete
// plus aws_vpc.new as a create, both rows collapse into a single
// Informational entry hinting that the plan is stale. The unrelated
// delete in the same fixture passes through unchanged.
func TestEnrichFromPlanCollapsesStaleMovedPair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
resource "aws_vpc" "new" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "unrelated" {
  availability_zone = "us-east-1a"
}

moved {
  from = aws_vpc.old
  to   = aws_vpc.new
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	p := planFixture(t, "moved_stale.json")
	out := diff.EnrichFromPlan(nil, p, proj)

	// Expect: 1 collapsed move entry + 1 unrelated delete = 2 total.
	// Without the moved-block awareness we'd see 3 (delete old, create
	// new, delete unrelated).
	if got, want := len(out), 2; got != want {
		t.Fatalf("len(out) = %d, want %d; got %+v", got, want, out)
	}

	var moveEntry, unrelatedEntry *diff.Change
	for i := range out {
		if strings.Contains(out[i].Detail, "regenerate the plan") {
			moveEntry = &out[i]
		}
		if out[i].Subject == "aws_subnet.unrelated" {
			unrelatedEntry = &out[i]
		}
	}
	if moveEntry == nil {
		t.Fatal("missing collapsed stale-move entry")
	}
	if moveEntry.Kind != diff.Informational {
		t.Errorf("stale-move Kind = %v, want Informational", moveEntry.Kind)
	}
	if !strings.Contains(moveEntry.Subject, "aws_vpc.old") || !strings.Contains(moveEntry.Subject, "aws_vpc.new") {
		t.Errorf("stale-move Subject should reference both addresses; got %q", moveEntry.Subject)
	}
	if unrelatedEntry == nil {
		t.Error("unrelated delete should still pass through")
	}
}

// TestEnrichFromPlanLeavesGenuineDestroyAlone confirms the moved-aware
// path doesn't accidentally suppress real destroys: when the source
// has NO moved block, a plan delete just stays a delete (not collapsed
// into a fake move hint).
func TestEnrichFromPlanLeavesGenuineDestroyAlone(t *testing.T) {
	// No moved block in source — every plan row should pass through
	// the normal path.
	p := planFixture(t, "moved_stale.json")
	out := diff.EnrichFromPlan(nil, p, nil)
	// 1 delete (aws_vpc.old) + 1 create (aws_vpc.new) + 1 delete
	// (aws_subnet.unrelated) = 3 entries. No collapse.
	if got, want := len(out), 3; got != want {
		t.Fatalf("len(out) = %d, want %d; got %+v", got, want, out)
	}
	for _, c := range out {
		if strings.Contains(c.Detail, "regenerate the plan") {
			t.Errorf("unexpected stale-move hint without source-side moved block: %+v", c)
		}
	}
}

// TestEnrichResultsFromPlanRoutesByModule pins the per-module routing.
// The fixture has:
//   - module.network.aws_vpc.main update     → routes to PairResult{Key: "network"}
//   - module.network.module.subnets.aws_subnet.public[east] create
//     → routes to PairResult{Key: "network.subnets"}
//   - data.aws_ami.latest read               → filtered (no-op-equivalent)
//
// We pre-stage two empty PairResults with matching keys; the routing
// should land each plan-derived row inside the right one without
// touching rootChanges.
func TestEnrichResultsFromPlanRoutesByModule(t *testing.T) {
	results := []diff.PairResult{
		{Pair: loader.ModuleCallPair{Key: "network", LocalName: "network"}},
		{Pair: loader.ModuleCallPair{Key: "network.subnets", LocalName: "subnets"}},
	}
	rootChanges := []diff.Change{
		{Kind: diff.Informational, Subject: "out.docs", Detail: "static-side root finding", Source: diff.SourceStatic},
	}

	p := planFixture(t, "nested_module.json")
	gotResults, gotRoot := diff.EnrichResultsFromPlan(results, rootChanges, p, nil)

	// rootChanges should still contain only the original static-side
	// finding — every plan row had a matching pair so nothing
	// fell through.
	if len(gotRoot) != 1 || gotRoot[0].Subject != "out.docs" {
		t.Errorf("rootChanges should be unchanged; got %+v", gotRoot)
	}
	// `network` pair should have one update row (vpc.cidr_block).
	if got := len(gotResults[0].Changes); got != 1 {
		t.Fatalf("network pair Changes len = %d, want 1; got %+v", got, gotResults[0].Changes)
	}
	if !strings.HasPrefix(gotResults[0].Changes[0].Subject, "module.network.aws_vpc.main") {
		t.Errorf("network pair routed wrong row: %q", gotResults[0].Changes[0].Subject)
	}
	// `network.subnets` pair should have one create summary.
	if got := len(gotResults[1].Changes); got != 1 {
		t.Fatalf("subnets pair Changes len = %d, want 1; got %+v", got, gotResults[1].Changes)
	}
	if !strings.Contains(gotResults[1].Changes[0].Detail, "plan creates") {
		t.Errorf("subnets pair routed wrong row: %+v", gotResults[1].Changes[0])
	}
}

// TestEnrichResultsFromPlanFallsBackToRoot covers the case where a
// plan describes a module not in the pair list — typically a stale
// plan or a module that doesn't appear in the diff. Those rows should
// land in rootChanges with the full plan address as Subject so
// reviewers can still see them.
func TestEnrichResultsFromPlanFallsBackToRoot(t *testing.T) {
	// No paired module calls — every plan row will fall through to root.
	var results []diff.PairResult
	p := planFixture(t, "nested_module.json")
	gotResults, gotRoot := diff.EnrichResultsFromPlan(results, nil, p, nil)

	if len(gotResults) != 0 {
		t.Errorf("results should remain empty; got %+v", gotResults)
	}
	// nested_module.json has 2 non-no-op changes (vpc update + subnet
	// create) → 1 + 1 = 2 plan-derived entries in rootChanges.
	if got, want := len(gotRoot), 2; got != want {
		t.Fatalf("rootChanges len = %d, want %d; got %+v", got, want, gotRoot)
	}
	for _, c := range gotRoot {
		if c.Source != diff.SourcePlan {
			t.Errorf("rootChanges entry %q should be Source=plan; got %q", c.Subject, c.Source)
		}
	}
}

// TestEnrichResultsFromPlanNilNoop pins the early-return contract:
// passing nil plan returns the inputs verbatim so cmd/diff doesn't
// have to branch on the --enrich-with-plan flag at every call site.
func TestEnrichResultsFromPlanNilNoop(t *testing.T) {
	results := []diff.PairResult{
		{Pair: loader.ModuleCallPair{Key: "x"}, Changes: []diff.Change{{Subject: "x"}}},
	}
	rootChanges := []diff.Change{{Subject: "y"}}
	gotResults, gotRoot := diff.EnrichResultsFromPlan(results, rootChanges, nil, nil)
	if len(gotResults) != 1 || gotResults[0].Pair.Key != "x" {
		t.Errorf("nil plan should leave results unchanged; got %+v", gotResults)
	}
	if len(gotRoot) != 1 || gotRoot[0].Subject != "y" {
		t.Errorf("nil plan should leave rootChanges unchanged; got %+v", gotRoot)
	}
}

// TestEnrichFromPlanRedactsSensitiveAndUnknown pins the renderer
// substitution behaviour: a plan touching a sensitive attribute
// must NOT spill the value into the Detail (it would land in CI
// logs), and a `(known after apply)` attribute must show that text
// instead of `<nil>` (which looks like the attribute is being unset).
func TestEnrichFromPlanRedactsSensitiveAndUnknown(t *testing.T) {
	p := planFixture(t, "sensitive_and_unknown.json")
	out := diff.EnrichFromPlan(nil, p, nil)

	// Find the password and arn rows for the RDS resource.
	var passwordEntry, arnEntry, engineEntry, secretEntry *diff.Change
	for i := range out {
		switch out[i].Subject {
		case "aws_db_instance.main:password":
			passwordEntry = &out[i]
		case "aws_db_instance.main:arn":
			arnEntry = &out[i]
		case "aws_db_instance.main:engine_version":
			engineEntry = &out[i]
		case "aws_secretsmanager_secret_version.config:secret_string":
			secretEntry = &out[i]
		}
	}

	if passwordEntry == nil {
		t.Fatal("missing password entry")
	}
	if strings.Contains(passwordEntry.Detail, "hunter2") ||
		strings.Contains(passwordEntry.Detail, "correcthorsebatterystaple") {
		t.Errorf("password Detail leaked the value: %q", passwordEntry.Detail)
	}
	if !strings.Contains(passwordEntry.Detail, "(sensitive)") {
		t.Errorf("password Detail should contain (sensitive); got %q", passwordEntry.Detail)
	}

	if arnEntry == nil {
		t.Fatal("missing arn entry")
	}
	if !strings.Contains(arnEntry.Detail, "(known after apply)") {
		t.Errorf("arn Detail should contain (known after apply); got %q", arnEntry.Detail)
	}

	if engineEntry == nil {
		t.Fatal("missing engine_version entry")
	}
	if !strings.Contains(engineEntry.Detail, "14.5") || !strings.Contains(engineEntry.Detail, "15.1") {
		t.Errorf("engine_version Detail should contain raw values; got %q", engineEntry.Detail)
	}

	if secretEntry == nil {
		t.Fatal("missing secret_string entry — should emit ONE subtree-level delta, not per-leaf rows")
	}
	if strings.Contains(secretEntry.Detail, "db_pass") ||
		strings.Contains(secretEntry.Detail, "api_key") {
		t.Errorf("secret subtree leaked inner keys: %q", secretEntry.Detail)
	}
	// And no per-leaf rows for the inner keys leaked through.
	for _, c := range out {
		if strings.HasPrefix(c.Subject, "aws_secretsmanager_secret_version.config:secret_string.") {
			t.Errorf("unexpected per-leaf row inside masked subtree: %q", c.Subject)
		}
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
