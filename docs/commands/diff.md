# `tflens diff`

The **author view**: "what changed in this module's API between this branch and the base ref?". Classifies every detected change as Breaking / Non-breaking / Informational, with one-line fix hints. Optional `--enrich-with-plan` folds `terraform show -json` output into the diff for attribute-level visibility.

## When to use it

- **PR review on a published module repo** — gate the merge on Breaking findings.
- **Release engineering** — sanity-check the API delta before tagging a new version.
- **Author-side audit** — see whether a refactor that "looks safe" actually preserves the contract for callers.

For the consumer-side question ("does this upgrade break MY caller?"), see [`whatif`](whatif.md).

## Usage

```
tflens diff [path] [--ref <base>] [--enrich-with-plan plan.json]
```

| Flag | Default | Notes |
|---|---|---|
| `path` | `.` | Project path. Single `.tf` file or directory. |
| `--ref` | `auto` | Git ref to compare against. `auto` resolves to `@{upstream}` → `origin/HEAD` → `main` → `master`. |
| `--enrich-with-plan` | _(empty)_ | Path to `terraform show -json` output. Folds plan-derived attribute deltas into the diff. See [Plan enrichment](#plan-enrichment) below. |

Exits non-zero when any Breaking finding (static-side OR plan-side) exists. Suitable for CI gating.

The root module gets a full API diff — adding a required variable, removing an output, changing the backend, etc. all show up under a `Root module:` section. The operator running `terraform plan` against this directory IS the consumer, even though no parent calls the root.

For child module calls, the classification depends on **how the child is sourced**:

- **Local children** (`source = "./…"` or `"../…"`) — internal to this repo. Their API is implementation detail; only the parent's actual consumption is observable. `diff` runs cross-validation of the new parent against the new child and reports a Breaking change only when the parent's usage is broken (passes an unknown arg, fails to pass a now-required input, or references a removed `module.<name>.<output>`). Renaming a variable that the parent updated atomically is silent.
- **Registry / git children** — published by someone else (or by you, in a release). The publisher owns breaking-change discipline. `diff` reports the full API diff (every variable / output / type / lifecycle change) classified as Breaking, NonBreaking, or Informational. A removed variable shows up regardless of whether your specific parent passed it.

## Examples

### Required variable added (Breaking)

Old:

```hcl
variable "vpc_cidr" { type = string default = "10.0.0.0/16" }
```

New:

```hcl
variable "vpc_cidr"     { type = string }                 # default removed
variable "subnet_count" { type = number }                 # new required input
```

```bash
tflens diff --ref main .
```

```
Root module:
  Breaking (2):
    variable.subnet_count: required variable added (no default)
      hint: add `default = ...` to make it optional, or document that callers must set it
    variable.vpc_cidr: default removed (variable now required)
      hint: add `default = ...` to make it optional, or document that callers must set it
```

### Variable type narrowing (Breaking)

```hcl
- variable "regions" { type = any }
+ variable "regions" { type = list(string) }
```

```
variable.regions: type narrowed (any → list(string)); some old values are now rejected
  hint: keep the wider type, or coordinate the type change with all callers
```

### Output renamed (Breaking)

```hcl
- output "vpc_id" { value = aws_vpc.main.id }
+ output "id"     { value = aws_vpc.main.id }
```

```
output.vpc_id: removed
output.id: added
  (also flagged as a possible rename — consider whether callers need updating)
```

### Resource renamed via `moved {}` (Informational)

```hcl
moved {
  from = aws_security_group.web
  to   = aws_security_group.frontend
}
```

```
resource.aws_security_group.web → resource.aws_security_group.frontend: rename handled by moved {} block
```

### `source` changed in a registry-child call

```hcl
- module "vpc" { source = "myorg/vpc/aws", version = "~> 1.0" }
+ module "vpc" { source = "myorg/vpc/aws", version = "~> 2.0" }
```

```
Module "vpc": (content changed)
  Breaking (1):
    module.vpc: version constraint narrowed; some previously-satisfying versions now rejected
      hint: review the upstream changelog and decide if you can pin tighter or need to widen
```

## Fix hints

Most breaking changes carry a one-line `hint:` with the conventional fix. Hints cover the common cases:

- **Required variable added** → suggest `default = ...`
- **Required object field added** → suggest `optional()`
- **Resource removed / renamed** → suggest `removed {}` / `moved {}` blocks with the exact entity IDs filled in
- **Backend changes** → `terraform init -migrate-state`
- **`count` ↔ `for_each` transitions** → migration steps
- **Sensitive-leak removals on outputs** → restore the `sensitive = true` flag or stop returning the value
- **The four cross-validate consumption errors** (unknown argument, required input missing, type mismatch, no such output)

The JSON output emits the same string under a `"hint"` key (omitted when empty).

## Plan enrichment

`tflens diff` is fundamentally a static analyser — it doesn't embed provider schemas, so changes to resource attributes (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`, `instance_type` modifications, force-new attribute changes) are normally invisible. The `--enrich-with-plan` flag bridges that gap by reading the JSON output of `terraform show -json <plan>`:

```bash
terraform plan -out=tfplan && terraform show -json tfplan > plan.json
tflens diff --ref main --enrich-with-plan plan.json
```

> **Module-developer CI note:** plan enrichment is best for **consumer-side CI** (where a real plan exists). For the module-developer workflow — running `tflens diff` in the module repo itself — the source-only [`# tflens:track` marker](tracked-attributes.md) is the right tool. It opts specific attributes into the diff with no plan, no credentials, no consuming workspace.

**Classification:** force-new attributes (those Terraform marks in the plan's `replace_paths`) become Breaking; other attribute changes become Informational; resource creates become Informational; resource deletes and explicit replaces (destroy + recreate) become Breaking. The CI exit code refreshes to include plan-derived Breaking findings — so a plan-only force-new change still gates the merge even when the source-side text doesn't differ.

**Provenance:** plan-derived rows are tagged with a `[plan]` prefix in the console renderer and a 📋 marker in the markdown renderer.

**Sensitive value redaction.** Attributes flagged via the plan's `before_sensitive` / `after_sensitive` shadow trees (anything that flowed through a sensitive variable, sensitive output, or `sensitive = true` resource attribute) render as `(sensitive)` instead of the raw value. Subtree-wide markers (e.g. an entire `aws_secretsmanager_secret_version.secret_string` map) collapse to a single `(sensitive)` row. No raw values can leak into CI logs.

**`(known after apply)` rendering.** Attributes whose post-apply value is computed by the provider (the plan's `after_unknown` shadow) render as `(known after apply)` rather than `<nil>`, so `aws_db_instance.main:arn` showing `→ (known after apply)` is unambiguously "the provider will fill this in" rather than "the attribute is being unset".

**Per-module routing.** Plan-derived rows whose module address matches a paired module call land under that pair's section in the rendered output, next to the static-side findings for the same module. Indexed module addresses (`module.regions["us-east-1"]`) route to the underlying `module.regions` pair regardless of how many instances the for_each / count produces.

**Source positions.** When a plan ResourceChange matches a known source-side entity, the entity's source position (`file:line`) is propagated onto the resulting Change so the markdown renderer can link plan-derived rows back to the resource declaration.

**Stale `moved {}` block detection.** When the source declares `moved { from = X; to = Y }` AND the plan still shows X as a delete plus Y as a create, the pair is collapsed into a single Informational entry hinting that the plan is stale and should be regenerated. Resource/data renames collapse a single (delete, create) pair. **Module-call renames** (`moved { from = module.old; to = module.new }`) collapse the entire cluster of N nested resources being destroyed under the old prefix and recreated under the new prefix — one Informational entry per moved-block declaration, with a count of nested resources. Partial matches inside a module-rename cluster are honest: a resource that's deleted from `module.old` but has no twin under `module.new` (e.g. removed during the same PR as the rename) flows through the normal path as its own finding.

### Plan-enrichment example output

```
Root module:
  Breaking (2):
    [plan] aws_vpc.main: plan replaces `aws_vpc.main` (destroy + recreate)
    [plan] aws_vpc.main:cidr_block: plan attribute change: "10.0.0.0/16" → "10.1.0.0/16"
      hint: this attribute forces a destroy + recreate; coordinate with the operator
```

### Plan-enrichment limitations

- **`count` / `for_each` instances all roll up to the same source-side entity.** Plan addresses like `aws_subnet.foo[0]` / `aws_subnet.foo["us-east-1"]` resolve to the single source-side `resource.aws_subnet.foo` declaration, but each instance still gets its own Change row with the full plan address (including the index) preserved in the Subject. So `aws_subnet.foo[0]:cidr_block` and `aws_subnet.foo[1]:cidr_block` are visually distinct in the output.
- **Plan format versions.** Supports format_version 1.x (Terraform 1.0+). Older plans are rejected with a clear error.

## What it catches

**Variables:**

| Change | Kind |
| --- | --- |
| Variable removed | Breaking |
| New required variable (no default) | Breaking |
| New optional variable (has default) | NonBreaking |
| `default` removed (optional → required) | Breaking |
| `default` added (required → optional) | NonBreaking |
| Type widened — every old value is still acceptable (`string` → `any`, `list(string)` → `list(any)`, …; backed by `cty.Convert`) | NonBreaking |
| Type narrowed — some old values are now rejected (`any` → `string`, `list(any)` → `list(string)`, …) | Breaking |
| Type changed and incompatible (unrelated shapes) | Breaking |
| Existing default still converts cleanly to the new type | Informational |
| Object field added (required) | Breaking |
| Object field added (optional) | NonBreaking |
| Object field removed | Breaking |
| Object field optional → required | Breaking |
| Object field required → optional | NonBreaking |
| Object field inner type widened | NonBreaking |
| Object field inner type narrowed/incompatible | Breaking |
| `nullable = false` added | Breaking |
| `nullable = false` removed | NonBreaking |
| `sensitive = true` added | Breaking |
| `sensitive = true` removed | Informational |
| `ephemeral = true` added (Terraform 1.10+) | Breaking |
| `ephemeral = true` removed | NonBreaking |
| `validation` / `precondition` / `postcondition` block added (compared by canonical condition text — reordering is a no-op) | Informational |
| `validation` / `precondition` / `postcondition` block removed | Informational ("loosens" the contract) |

**Outputs:**

| Change | Kind |
| --- | --- |
| Output removed | Breaking |
| Output added | NonBreaking |
| `sensitive = true` added | Informational |
| `sensitive = true` removed (sensitive leak — value previously masked is now exposed) | Breaking |
| `ephemeral = true` added (Terraform 1.10+) | Breaking |
| `ephemeral = true` removed | NonBreaking |
| Output value type narrowed or incompatible | Breaking |
| `value` expression changed but inferred type compatible (or types unknown) | Informational |
| Referenced `local` expression changed (indirect, surfaced when the output expression text is unchanged) | Informational |
| `precondition` / `postcondition` block added or removed | Informational |
| `depends_on` changed | Informational |

**Resources, data sources, module calls:**

| Change | Kind |
| --- | --- |
| Entity removed | Breaking |
| Entity added | Informational |
| Rename detected (1:1 same kind+type, 1 removed + 1 added) | Breaking ("possible rename") |
| `moved { from = X, to = Y }` block covering the pair | Informational ("rename handled") |
| `removed { from = X }` block covering a deletion | Informational ("removal handled") |
| `count` ↔ `for_each` transition (either direction) | Breaking |
| `count` / `for_each` added to or removed from a singleton | Breaking |
| `for_each` *key type* narrowed or incompatible | Breaking |
| `for_each` *expression* changed but key type compatible | Informational |
| `count` *expression* infers as a non-number type | Breaking |
| `count` *expression* changed but stays numeric | Informational |
| `provider = aws.east` → `aws.west` | Breaking |
| Lifecycle: `prevent_destroy` added/removed | Informational |
| Lifecycle: `create_before_destroy` toggled | Informational |
| Lifecycle: `ignore_changes = all` narrowed to a list | Breaking |
| Lifecycle: `ignore_changes` widened to `all` | NonBreaking |
| Lifecycle: `ignore_changes` / `replace_triggered_by` content changed (otherwise) | Informational |
| Lifecycle: `precondition` / `postcondition` block added or removed | Informational |
| `depends_on` changed | Informational |
| Module `source` changed | Informational |
| Module `version` constraint changed (semver-aware) | Breaking / NonBreaking / Informational |
| Module argument added/removed/value-changed | Informational |

**`terraform {}` block:**

| Change | Kind |
| --- | --- |
| `required_version` constraint change (semver-aware) | Breaking / NonBreaking / Informational |
| `required_providers` entry added | Breaking |
| `required_providers` entry removed | NonBreaking |
| Provider `source` changed | Breaking |
| Provider `version` constraint change (semver-aware) | Breaking / NonBreaking / Informational |
| `backend` block added or removed (state migrates; `terraform init -migrate-state` required) | Breaking |
| `backend` type changed (`s3` → `azurerm`, etc.) | Breaking |
| `backend` config attribute added, removed, or value changed | Breaking |

**Semver-aware version constraint comparison:**

Version constraints (`>= 1.0`, `~> 4.0`, `!= 1.2.3`, compound forms like `">= 1.0, < 2.0"`) are parsed into interval sets and compared:

- **Equal** — same satisfying set
- **Broadened** (old ⊂ new) — NonBreaking
- **Narrowed** (new ⊂ old) — Breaking ("tightened")
- **Overlap** (neither subset, some overlap) — Breaking ("partially narrowed")
- **Disjoint** — Breaking ("incompatible")
- **Unparseable** — falls back to Informational with a generic detail

## What it does NOT catch

- **Resource attribute body changes.** A resource gaining or changing an attribute (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) is not diffed by default. The noise-to-signal ratio of a full body diff is too low. Authors can opt specific attributes into the diff with the [`# tflens:track`](tracked-attributes.md) marker.
- **Resource provider schema changes.** Cannot tell that an AWS provider bump from v4 to v5 silently changed `aws_vpc.main.cidr_block` from a string to a list.
- **Dynamic block content.** `dynamic "ingress" { for_each = ... }` bodies generate blocks at plan time. Without evaluating the `for_each`, the generated block set is opaque.
- **Condition strictness.** If `validation { condition = length(var.x) > 0 }` becomes `condition = length(var.x) > 5`, the validation block is recorded in both versions but we can't tell that the new condition rejects strictly more inputs.
- **Default value *content* changes.** Only the presence or absence of a default is diffed by default. When the variable is referenced from a tracked attribute (`# tflens:track`), the default IS followed.
- **Description / documentation changes.** Informational-only, currently skipped.
- **Ambiguous renames.** When there are multiple removed + added candidates of the same kind+type, no pairing is attempted — each is reported individually.
- **Type coercion subtleties.** `list(string)` → `set(string)` is flagged as a type change even though Terraform auto-converts in some expression contexts.
- **`check { assert { ... } }` blocks** (Terraform 1.5+) — not currently parsed.
- **`import { ... }` blocks** (Terraform 1.5+) — not currently parsed.
- **Provisioner blocks** (`provisioner "local-exec"`, `connection`) — not currently parsed.
- **Nested moved-block expressions with indices.** `moved { from = aws_vpc.main[0], to = aws_vpc.main["a"] }` is not recognised; only bare resource references in `from` / `to` are parsed.
- **Children that cannot be resolved offline.** In `--offline` mode or against registry/git sources missing from the cache and from `.terraform/modules/modules.json`, the child is skipped rather than reported.

## Output formats

`--format json` emits a structured envelope per module pair with breaking/non-breaking/info totals:

```json
{
  "base_ref": "origin/main",
  "path": ".",
  "modules": [
    {
      "key": "vpc",
      "status": "changed",
      "changes": [
        { "kind": "breaking", "subject": "variable.region", "detail": "...", "hint": "...", "source": "static" }
      ]
    }
  ],
  "summary": { "breaking": 1, "non_breaking": 0, "informational": 0 }
}
```

`--format markdown` renders for PR-comment use (severity badges 🔴🟡🔵, collapsible `<details>` per module, fix hints). Pipe directly to `gh pr comment` or use the [GitHub Action](../../README.md#github-action) for sticky-comment plumbing.

## Related

- **[`docs/commands/whatif.md`](whatif.md)** — consumer view ("does this break MY caller?").
- **[`docs/commands/tracked-attributes.md`](tracked-attributes.md)** — opt resource attributes into the diff with `# tflens:track` markers; the source-only equivalent of `--enrich-with-plan` for module-developer CI.
- **[`docs/commands/statediff.md`](statediff.md)** — the operator-side companion: which state instances may be destroyed?
- **[main README — GitHub Action](../../README.md#github-action)** — one-line CI integration with sticky PR comments.
