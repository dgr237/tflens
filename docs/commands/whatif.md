# `tflens whatif`

The **consumer view**: "if I merged the working tree's module changes, would my parent still work?". Strictly more focused than `diff` — a child can ship many Breaking API changes that don't affect a particular caller. `whatif` cross-validates the parent's argument set against the candidate child's variables and only flags changes that *actually break this caller*.

## When to use it

- **Module-call upgrades** — gating a PR strictly on "will this break MY usage?".
- **Multi-tenant module repos** — when one consumer's PR shouldn't be blocked by another consumer's break.
- **Companion to `diff`** when you want both perspectives on the same PR — `diff` for "anything changed I should know about?", `whatif` for "anything changed that breaks ME?".

## Usage

```
tflens whatif [path] [module-call-name] [--ref <base>] [--enrich-with-plan plan.json]
```

| Flag / arg | Default | Notes |
|---|---|---|
| `path` | `.` | Project path. |
| `module-call-name` | _(empty)_ | Optional — scopes to one specific module call. |
| `--ref` | `auto` | Git ref to compare against. `auto` resolves to `@{upstream}` → `origin/HEAD` → `main` → `master`. |
| `--enrich-with-plan` | _(empty)_ | Layer plan-derived findings into each call's API-changes section. See [Plan enrichment](#plan-enrichment) below. |

Exits non-zero when the direct-impact list is non-empty (suitable for CI gating). With `--enrich-with-plan`, plan-derived Breaking findings count toward the gate too.

For every module call in `path` that differs between the working tree and the base ref, `whatif` loads the parent at base, loads the candidate child from the working tree, and reports:

1. **Direct impact** — cross-validation of the parent's `module "<name>" { ... }` block against the candidate child. Missing required inputs, unknown arguments, type mismatches the upgrade would introduce.
2. **Module API changes** — the full `diff` between the base and working-tree child, for context.

## `whatif` vs `diff`

Both compare the path against a git base. The difference is on **registry / git children**:

| Child source | `diff` | `whatif` |
|---|---|---|
| Local (`./…`, `../…`) | Consumer view (cross-validate parent vs new child) | Consumer view (same) |
| Registry / git | Author view (full API diff classified by API-shape rules) | Consumer view (cross-validate parent vs new child; suppresses changes that don't affect this caller) |

In CI:

- `tflens diff` for the broad question "anything changed I should know about?" — quiet on local internals you've updated atomically, loud on registry-module API drift.
- `tflens whatif` when you want to gate a PR strictly on "will this break MY usage?" — quiet on every module-call upgrade that your parent absorbs cleanly, regardless of how the child is sourced.

## Examples

### A clean upgrade (silent)

Parent on `main`:

```hcl
module "vpc" {
  source  = "myorg/vpc/aws"
  version = "~> 1.0"
  cidr    = "10.0.0.0/16"
}
```

Child v1.5.0 (working tree) adds an optional variable but otherwise preserves the contract:

```hcl
variable "cidr" { type = string }
variable "enable_flow_logs" {
  type    = bool
  default = false   # new, optional, has default
}
```

```bash
tflens whatif --ref main .
```

Output: nothing. Exit 0. The parent's `cidr` argument still type-checks, no required input is missing, no removed output was referenced. `tflens diff` against the same change would surface the new variable as Informational; `whatif` correctly suppresses it.

### Upgrade that breaks the caller

Child v2.0.0 renames `cidr` → `vpc_cidr`:

```hcl
- variable "cidr" { type = string }
+ variable "vpc_cidr" { type = string }
```

```
Module call "vpc": (would break)
  Direct impact (2):
    module.vpc: unknown argument "cidr" — child module has no variable "cidr" (did you mean "vpc_cidr"?)
      hint: remove the argument from the module block, or restore the matching variable in the child
    module.vpc: required input "vpc_cidr" not provided
      hint: add the input to the module block, or give the child variable a default
  API changes (context):
    Breaking (2): variable.cidr removed; variable.vpc_cidr added (no default)
```

### Scoped to one call

```bash
tflens whatif . vpc --ref main
```

Same output as above, but only the `vpc` call is simulated. Useful in monorepos where one PR touches multiple module callers and you want to gate per-team.

### Output type narrowed (Breaking only when the parent uses it)

Child changes:

```hcl
- output "subnet_ids" { value = aws_subnet.public[*].id }
+ output "subnet_ids" {
+   value = aws_subnet.public[*].id
+   sensitive = true   # was not sensitive before
+ }
```

If the parent does:

```hcl
output "downstream" {
  value = module.network.subnet_ids
  # sensitive flag NOT propagated
}
```

→ `whatif` flags the parent's `output "downstream"` as a sensitive-leak. If the parent doesn't reference `subnet_ids` at all, nothing is flagged.

## Plan enrichment

`whatif --enrich-with-plan plan.json` layers plan-derived findings (force-new attribute changes, replaces, deletes) onto each call's API-changes section, so reviewers see one merged view per call:

```bash
terraform plan -out=tfplan && terraform show -json tfplan > plan.json
tflens whatif --ref main --enrich-with-plan plan.json
```

Plan rows whose module address has no matching call are dropped silently — `whatif` is per-call only. For root-level coverage, use [`tflens diff --enrich-with-plan`](diff.md#plan-enrichment) instead.

Plan-derived Breaking findings count toward the CI exit code in addition to DirectImpact: a force-new attribute change in a child IS a consumer concern even when the parent's USE cross-validates cleanly (the resource will physically rebuild).

The DirectImpact list is NOT modified — it stays strictly the cross-validation result. Plan deltas appear under "API changes (context)" tagged with `[plan]` / 📋 so reviewers can tell the signal sources apart.

## Output formats

`--format json` emits a per-call array with the gating signal under `direct_impact` and context under `api_changes`:

```json
{
  "base_ref": "origin/main",
  "path": ".",
  "calls": [
    {
      "key": "vpc",
      "direct_impact": [
        { "entity": "module.vpc", "msg": "unknown argument \"cidr\"", "pos": {...} }
      ],
      "api_changes": [
        { "kind": "breaking", "subject": "variable.cidr", "detail": "removed", "source": "static" }
      ]
    }
  ],
  "summary": { "breaking": 2, "non_breaking": 0, "informational": 0, "direct_impact": 2 }
}
```

`--format markdown` renders for PR-comment use with collapsible `<details>` per call.

## Related

- **[`docs/commands/diff.md`](diff.md)** — author view; broader scope.
- **[`docs/commands/statediff.md`](statediff.md)** — operator view; what state may be destroyed by the merge.
- **[`docs/commands/tracked-attributes.md`](tracked-attributes.md)** — markers in the child are also resolved through the parent's call argument, so cross-module tracked-attribute changes surface in `whatif` too.
- **[main README — GitHub Action](../../README.md#github-action)** — one-line CI integration.
