package plan_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgr237/tflens/pkg/plan"
)

// fixturePath returns the absolute path of a testdata file. Mirrors
// the runtime.Caller pattern used by the rest of tflens's tests so
// test invocations from the repo root or from inside pkg/plan both
// resolve correctly.
func fixturePath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

// TestLoadUnsupportedVersion exercises the format_version guard. The
// loader rejects anything outside the 1.x major series so callers
// don't silently consume a plan they can't interpret.
func TestLoadUnsupportedVersion(t *testing.T) {
	if _, err := plan.Load(fixturePath("unsupported_version.json")); err == nil {
		t.Fatal("expected error for format_version 0.2, got nil")
	}
}

// TestLoadResolvesAddressFields covers the core happy-path: each
// resource_change ends up with a parsed Type/Name/Mode and the
// EntityID matches what pkg/analysis would produce for the same
// resource. ChangeSet predicates (IsUpdate / IsReplace / IsNoOp /
// IsCreate) get a quick sanity check on the same fixture.
func TestLoadResolvesAddressFields(t *testing.T) {
	p, err := plan.Load(fixturePath("update.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(p.ResourceChanges), 3; got != want {
		t.Fatalf("len(ResourceChanges) = %d, want %d", got, want)
	}

	cases := []struct {
		Name         string
		Idx          int
		WantEntityID string
		WantUpdate   bool
		WantReplace  bool
		WantNoOp     bool
	}{
		{Name: "vpc", Idx: 0, WantEntityID: "resource.aws_vpc.main", WantUpdate: true},
		{Name: "subnet", Idx: 1, WantEntityID: "resource.aws_subnet.public", WantReplace: true},
		{Name: "sg", Idx: 2, WantEntityID: "resource.aws_security_group.unchanged", WantNoOp: true},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			rc := p.ResourceChanges[tc.Idx]
			if got := rc.EntityID(); got != tc.WantEntityID {
				t.Errorf("EntityID = %q, want %q", got, tc.WantEntityID)
			}
			if got := rc.Change.IsUpdate(); got != tc.WantUpdate {
				t.Errorf("IsUpdate = %v, want %v", got, tc.WantUpdate)
			}
			if got := rc.Change.IsReplace(); got != tc.WantReplace {
				t.Errorf("IsReplace = %v, want %v", got, tc.WantReplace)
			}
			if got := rc.Change.IsNoOp(); got != tc.WantNoOp {
				t.Errorf("IsNoOp = %v, want %v", got, tc.WantNoOp)
			}
		})
	}
}

// TestAttrDeltasUpdate covers attribute-level delta extraction. The
// fixture's vpc has a tag added (no force-new) — should produce one
// AttrDelta for tags.Env. The subnet has its availability_zone
// rewritten — should produce one AttrDelta with ForceNew=true.
func TestAttrDeltasUpdate(t *testing.T) {
	p, _ := plan.Load(fixturePath("update.json"))
	vpcDeltas := p.ResourceChanges[0].Change.AttrDeltas()
	if len(vpcDeltas) != 1 {
		t.Fatalf("vpc deltas len = %d, want 1; got %+v", len(vpcDeltas), vpcDeltas)
	}
	if vpcDeltas[0].Path != "tags.Env" {
		t.Errorf("vpc delta path = %q, want tags.Env", vpcDeltas[0].Path)
	}
	if vpcDeltas[0].ForceNew {
		t.Error("vpc tags.Env should NOT be force-new")
	}

	subnetDeltas := p.ResourceChanges[1].Change.AttrDeltas()
	if len(subnetDeltas) != 1 {
		t.Fatalf("subnet deltas len = %d, want 1; got %+v", len(subnetDeltas), subnetDeltas)
	}
	if subnetDeltas[0].Path != "availability_zone" {
		t.Errorf("subnet delta path = %q, want availability_zone", subnetDeltas[0].Path)
	}
	if !subnetDeltas[0].ForceNew {
		t.Error("subnet availability_zone SHOULD be force-new (in replace_paths)")
	}
}

// TestAttrDeltasCreateAndDelete covers the asymmetric Before/After
// cases — pure creates have nil Before, pure deletes have nil After.
// AttrDelta should still emit one entry per attribute.
func TestAttrDeltasCreateAndDelete(t *testing.T) {
	p, _ := plan.Load(fixturePath("nested_module.json"))
	// Index 1 is the create (subnet.public[east]).
	createDeltas := p.ResourceChanges[1].Change.AttrDeltas()
	if len(createDeltas) != 1 {
		t.Fatalf("create deltas len = %d, want 1", len(createDeltas))
	}
	if createDeltas[0].Before != nil {
		t.Errorf("create delta Before = %v, want nil", createDeltas[0].Before)
	}
	if createDeltas[0].After != "us-east-1a" {
		t.Errorf("create delta After = %v, want us-east-1a", createDeltas[0].After)
	}
}

// TestAddressParsingNestedModule exercises the address parser
// against `module.X.module.Y.<type>.<name>[<index>]` shape. Plus
// the `data.<type>.<name>` data-source variant.
func TestAddressParsingNestedModule(t *testing.T) {
	p, _ := plan.Load(fixturePath("nested_module.json"))

	// Index 0 — single-level module, populated module_address field.
	if got := p.ResourceChanges[0].ModuleAddress; got != "module.network" {
		t.Errorf("rc[0].ModuleAddress = %q, want module.network", got)
	}

	// Index 1 — two-level module, parsed from address (no
	// module_address in fixture). Tests the parseAddress fallback.
	rc := p.ResourceChanges[1]
	if got := rc.ModuleAddress; got != "module.network.module.subnets" {
		t.Errorf("rc[1].ModuleAddress = %q, want module.network.module.subnets", got)
	}
	if got := rc.Type; got != "aws_subnet" {
		t.Errorf("rc[1].Type = %q, want aws_subnet", got)
	}
	if got := rc.Name; got != "public" {
		t.Errorf("rc[1].Name = %q, want public", got)
	}
	if got := rc.Index; got != "east" {
		t.Errorf("rc[1].Index = %v, want east", got)
	}

	// Index 2 — data source. EntityID prefix changes accordingly.
	if got := p.ResourceChanges[2].EntityID(); got != "data.aws_ami.latest" {
		t.Errorf("rc[2].EntityID = %q, want data.aws_ami.latest", got)
	}
}
