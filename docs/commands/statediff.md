# `tflens statediff`

The **operator view**: "if I merge this branch, which of my state's resource instances are at risk?". A static hazard detector for PRs that may unintentionally add, destroy, or re-instance resources. Pure static analysis — no Terraform execution, no provider schemas, no plan required (though `--enrich-with-plan` corroborates with concrete plan-side actions when available).

## When to use it

- **PR review on a workspace** — gate the merge on resources that may be destroyed or re-instanced.
- **Pre-deploy sanity check** — before applying a branch, list the state instances that could be affected.
- **Auditing sensitive locals** — find the locals or variable defaults whose changes would silently shrink a `count` or `for_each` expansion.

## Usage

```
tflens statediff [path] [--ref <base>] [--state state.json] [--enrich-with-plan plan.json]
```

| Flag | Default | Notes |
|---|---|---|
| `path` | `.` | Project path. |
| `--ref` | `auto` | Git ref to compare against. `auto` resolves to `@{upstream}` → `origin/HEAD` → `main` → `master`. |
| `--state` | _(empty)_ | Optional Terraform state v4 JSON file. When given, every flagged resource is annotated with the concrete state instances currently tracked. |
| `--enrich-with-plan` | _(empty)_ | Optional `terraform show -json` plan file. For every AffectedResource, attaches the per-instance plan actions terraform will take. See [Plan enrichment](#plan-enrichment) below. |

Exits non-zero when any resource add/remove or sensitive-local change fires. Renames (via `moved {}`) and state orphans are reported but do NOT contribute to the gate.

## What it reports

- **Resource identity adds and removes** — every `resource "TYPE" "NAME"` declaration that appears in one branch but not the other. A missing declaration is the most direct path to a destroy.
- **Renames via `moved {}` blocks** — a `moved { from = aws_vpc.old, to = aws_vpc.new }` block in the new tree that pairs a real removal with a real addition is recognised as a rename, listed separately, and does NOT contribute to the exit code (Terraform handles the rename without destroy/recreate).
- **Sensitive value changes** — locals OR variable defaults whose expression changed between branches, whose dependency chain reaches a `count` or `for_each` meta-argument. This is the silent class of bug: trim a list in `locals { regions = [...] }` (or drop `variable "n" { default = 3 }` to `1`) and a resource that expands from that value quietly loses instances. No attribute in the resource block itself changed — tools that only diff resource blocks miss it.
- **State cross-reference** — when `--state state.json` is given, every flagged resource is annotated with the instances currently in state. A reviewer sees concrete addresses (`aws_instance.web["us-west-2"]`) rather than abstract warnings.
- **State orphans** — addresses in state that have no corresponding declaration in the working tree. These indicate pre-existing drift and are reported separately; they do NOT contribute to the exit code since they are not caused by this PR.

## Examples

### A trimmed `locals` list silently destroys instances

Old:

```hcl
locals {
  regions = ["us-east-1", "us-west-2", "eu-west-1"]
}

resource "aws_instance" "web" {
  for_each = toset(local.regions)
  ami      = "ami-x"
}
```

New (one region removed):

```hcl
locals {
  regions = ["us-east-1", "us-west-2"]    # eu-west-1 removed
}
```

```bash
tflens statediff --ref main --state terraform.tfstate .
```

```
Value changes that may alter count/for_each expansion:
  - local.regions
      old: ["us-east-1","us-west-2","eu-west-1"]
      new: ["us-east-1","us-west-2"]
    Affected: aws_instance.web (for_each)
      • state instance: aws_instance.web["eu-west-1"]
```

The state-instance list tells the reviewer exactly which production resource is about to be destroyed. Without `--state`, the abstract `aws_instance.web (for_each)` line still flags the hazard.

### A removed resource (Breaking)

Old:

```hcl
resource "aws_security_group" "legacy_db" {
  name = "legacy"
  # ...
}
```

New: declaration removed, but no `removed {}` block.

```
Resource identity changes vs origin/main:
  - aws_security_group.legacy_db (managed)
```

Exits non-zero. Fix: either restore the resource, or wrap in a `removed { from = aws_security_group.legacy_db }` block (which would re-classify as Informational).

### A rename via `moved {}` (Informational, no gate)

```hcl
moved {
  from = aws_vpc.old
  to   = aws_vpc.new
}

resource "aws_vpc" "new" {
  cidr_block = "10.0.0.0/16"
}
```

```
Renames (moved block handled — no destroy/recreate):
  aws_vpc.old → aws_vpc.new
```

Exit code stays 0. Without the moved block, the same edit would surface as one removed + one added resource — a Breaking pair.

### Variable default reaches `count` (Breaking)

```hcl
- variable "subnet_count" { type = number  default = 3 }
+ variable "subnet_count" { type = number  default = 1 }
```

```
Value changes that may alter count/for_each expansion:
  - variable.subnet_count
      old: 3
      new: 1
    Affected: aws_subnet.public (count)
      • state instance: aws_subnet.public[1]
      • state instance: aws_subnet.public[2]
```

The state cross-reference lists the two instances that will be destroyed by the apply. Without `--state`, the abstract `aws_subnet.public (count)` line still flags the hazard but doesn't enumerate.

## Plan enrichment

`statediff --enrich-with-plan plan.json` is the highest-leverage flag combination. Statediff's static analysis flags resources whose `count` / `for_each` expression depends on a changed local or variable default — the **"this CAN recompute"** signal. Plan enrichment pairs that with the plan's **"here are the N concrete instances that WILL be affected"**.

```bash
terraform plan -out=tfplan && terraform show -json tfplan > plan.json
tflens statediff --ref main --enrich-with-plan plan.json
```

Each `AffectedResource` gets a `PlanInstances` list with per-instance plan addresses (including count/for_each indices) and the actions terraform will take (`["update"]`, `["delete", "create"]`, etc.):

```
Value changes that may alter count/for_each expansion:
  - variable.subnet_count
      old: 3
      new: 1
    Affected: aws_subnet.public (count)
      • plan: aws_subnet.public[1] [delete]
      • plan: aws_subnet.public[2] [delete]
```

That turns "this change touches a count expression" into "this change destroys aws_subnet.public[1] and aws_subnet.public[2]" — much more actionable for the reviewer.

Resources with no plan match leave `PlanInstances` empty. Possible reasons: count expanded to 0, plan is stale, or terraform didn't touch the resource.

> **Note on moved blocks.** `diff` and `whatif` collapse stale `moved {}` blocks (source declares the rename but the plan still shows destroy + create) into a single Informational entry — including module-call renames. `statediff --enrich-with-plan` does NOT do that collapse: its enrichment is purely about correlating the static-side `AffectedResource` items with concrete plan instances (a different shape). Statediff's static side ALREADY recognises moved-block renames and lists them under "Renames (moved block handled)" with no exit-code impact, so the moved-block / stale-plan case is covered by the static analysis rather than the plan-enrichment path here.

## What it does NOT do

- **Plan simulation.** Attribute-level diffs (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) need provider schemas and expression evaluation — that's `terraform plan`'s job. `statediff` is a complementary, cheap, schema-free check that catches a different class of hazard than plan.
- **Full expression evaluation.** A curated subset of Terraform built-ins is wired in (see [main README — Static evaluation surface](../../README.md#static-evaluation-surface)), so e.g. `count = length(local.xs)` IS evaluated when `local.xs` is statically known. But anything that reaches a data source, a computed resource attribute, or a non-curated function falls back to "may change". The signal is a warning, not a definitive answer.
- **Variable-driven changes across module callers.** A `count = var.n` resource where the caller on one branch passes `n = 3` and the other passes `n = 1` is not currently followed across module boundaries.

## Output formats

`--format json` emits the full Result struct. `AffectedResource.plan_instances` is omitted when empty, so adding `--enrich-with-plan` is backward-compatible for existing JSON consumers:

```json
{
  "base_ref": "origin/main",
  "path": ".",
  "added_resources": [],
  "removed_resources": [],
  "renamed_resources": [],
  "sensitive_changes": [
    {
      "module": "",
      "kind": "variable",
      "name": "subnet_count",
      "old_value": "3",
      "new_value": "1",
      "affected_resources": [
        {
          "module": "",
          "type": "aws_subnet",
          "name": "public",
          "mode": "managed",
          "meta_arg": "count",
          "state_instances": ["aws_subnet.public[1]", "aws_subnet.public[2]"],
          "plan_instances": [
            { "address": "aws_subnet.public[1]", "actions": ["delete"] },
            { "address": "aws_subnet.public[2]", "actions": ["delete"] }
          ]
        }
      ]
    }
  ]
}
```

`--format markdown` renders for PR-comment use, with each affected resource collapsed under a `<details>` summary listing the state and plan instances.

## Related

- **[`docs/commands/diff.md`](diff.md)** — API-surface changes (variables, outputs, types, module sources). Statediff catches the orthogonal class — state-instance hazards.
- **[`docs/commands/whatif.md`](whatif.md)** — does THIS upgrade break MY caller? (consumer view rather than operator view).
- **[main README — Static evaluation surface](../../README.md#static-evaluation-surface)** — the curated stdlib functions that `statediff` evaluates statically.
