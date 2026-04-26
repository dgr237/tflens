# `# tflens:track` — tracked attributes

Not a command; a comment marker that opts specific resource attributes into the [`tflens diff`](diff.md) output. The source-only alternative to `--enrich-with-plan` — runs in module-developer CI without a plan, credentials, or any consuming workspace.

## When to use it

`diff` deliberately ignores most resource-block attribute changes — they're noise relative to the public API surface. But some attributes are load-bearing for *operations*. Two broad classes are worth pulling out:

- **In-place but disruptive** — an EKS `cluster_version` bump, an RDS `engine_version` jump, an EC2 `instance_class` resize. The resource is updated in place but the change has real-world consequences (downtime, add-on compatibility, cost).
- **Force-new (destroy + recreate)** — an EKS `cluster_name`, an RDS `identifier`, an `aws_db_subnet_group.name`. Terraform's plan will show `# forces replacement` for these, but only after `terraform plan` runs against an applied state. At PR-review time the diff looks like an innocent string change. Worse, the value is usually computed (`"${var.env}-${local.suffix}"`), so the literal text in the resource block doesn't change at all when `local.suffix` flips from `"primary"` to `"secondary"`.

These are easy to merge by accident and hard to roll back. Module developers own the resource modules; they're the people who know which attributes need a second pair of eyes.

## Why it exists (and why not just `--enrich-with-plan`?)

`tflens` does NOT load provider schemas — no `terraform init`, no provider binaries, no credentials, no network. Loading them would defeat the "runs in seconds, no setup" property that makes tflens viable in module-developer CI. Without provider schemas, tflens has no way to independently know which attributes are force-new, sensitive, or computed — that knowledge lives inside the provider plugin.

The marker is the explicit alternative: the module developer encodes their domain knowledge in source — *"this attribute is part of my API contract"* — and the dev knows their module's contract better than any external schema can.

`--enrich-with-plan` is the consumer-side corroboration when a plan is available. It's harder to wire into module-dev CI because it needs the module to already exist at the upstream ref AND a plan generated against the new version. The track marker has none of those constraints — it works from source against any git ref.

## Quick example

```hcl
resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = "1.28" # tflens:track: bump only after add-on compatibility check
}
```

Now `tflens diff` flags any change to `cluster_version` as **Breaking**, with the comment text surfaced as the hint:

```
resource.aws_eks_cluster.this.cluster_version: changed "1.28" → "1.29"
  hint: bump only after add-on compatibility check
```

## Marker placement

| Form | Where it goes | Annotates |
|---|---|---|
| Trailing | Same line as the attribute, after the value | The attribute on that line |
| Own-line | On its own line, immediately above the attribute | The attribute on the next line |

Both `#` and `//` comment styles work. The text after `tflens:track:` is free-form — keep it short and operational; it appears verbatim in the diff hint.

```hcl
# Trailing
cluster_version = "1.28" # tflens:track: requires planned maintenance window

# Own-line — useful when the value is long
# tflens:track: requires planned maintenance window
cluster_version = "1.28"
```

A bare `# tflens:track` (no description) is also valid; the diff still flags changes, just without a custom hint.

## Indirection through variables and locals

Real modules rarely hard-code these values. The marker follows indirection one or two hops deep:

```hcl
variable "cluster_version" {
  type    = string
  default = "1.28"
  validation {
    condition     = contains(["1.28", "1.29", "1.30"], var.cluster_version)
    error_message = "version must be a supported EKS minor"
  }
}

resource "aws_eks_cluster" "this" {
  cluster_version = var.cluster_version # tflens:track
}
```

A change to `var.cluster_version`'s `default` is detected and reported as Breaking against the tracked attribute, with the variable's ID in the message. The same applies to `local.foo`, including chains (`local.outer = local.inner = "1.28"`) — the resolver recurses with cycle protection.

Combine with a `validation { condition = contains([...], var.cluster_version) }` block for two layers of safety: tflens flags the change at PR time, Terraform itself rejects unsupported values at plan time.

### Force-new attributes with computed values

The indirection rule extends to string interpolation, which is where force-new attributes usually hide:

```hcl
variable "env" {
  type    = string
  default = "prod"
}

locals {
  suffix = "primary"
}

resource "aws_eks_cluster" "this" {
  # cluster_name is force-new — changing this destroys and recreates the cluster
  cluster_name = "${var.env}-${local.suffix}" # tflens:track: force-new — destroys and recreates the cluster
}
```

If a teammate changes `local.suffix = "secondary"` in a follow-up PR, the literal text of `cluster_name` is unchanged — the resource block looks identical between branches. But the *computed* value flips from `"prod-primary"` to `"prod-secondary"`, and Terraform will plan a destroy + recreate. tflens follows the interpolated `var.env` and `local.suffix` references and reports:

```
resource.aws_eks_cluster.this.cluster_name: local.suffix changed: "primary" → "secondary"
  hint: force-new — destroys and recreates the cluster
```

Same mechanism, different operational risk profile — and the marker description (the text after `tflens:track:`) is the right place to communicate that risk to reviewers.

## Why removing the marker is itself flagged

If a teammate decides to "just remove the comment" to avoid the diff, that's exactly the failure mode the marker exists to prevent. Marker removal is reported as a Breaking change of its own:

```
resource.aws_eks_cluster.this.cluster_version: tracked-attribute marker removed (the safety guard is gone)
  hint: restore the `# tflens:track` comment, or remove the attribute entirely if the resource is gone
```

Adding a new marker is reported as Informational on its own — but if the underlying value also moved in the same PR (the common "I'm calling out this specific change" flow), it's promoted to Breaking with the old → new value shown:

```
local.cluster_version.value: tracked-attribute marker added; value "1.34" → "1.35"
  hint: EKS minor — bump only after add-on compatibility check
```

So you can introduce both the marker and the breaking change in one PR and still gate CI on the result.

## Where to put the marker

Pick the highest-leverage spot for your scenario:

- **Resource attribute** in the module that owns the resource — `cluster_version = "1.28" # tflens:track`. Catches direct edits to the literal AND changes to any `var.X` / `local.X` referenced in the value. Best for self-contained modules.
- **Locals block** in the parent that decides the value — `locals { cluster_version = "1.34" # tflens:track }`. Best when the local is the source of truth and the value is consumed by one or more module calls. Each local becomes its own tracked entity (`local.<name>.value`), and the indirection walker still resolves any `var.X` it references.
- **Module call argument** in the parent — `module "eks" { cluster_version = local.cluster_version # tflens:track }`. Best when the value flows through a parent that you own but the child is a third-party module. The walker follows the local back to its definition + any vars referenced inside.

### Cross-module resolution

When `tflens diff` runs against a project that contains module calls, a marker in a child module is also resolved through the parent's call argument. A marker on `cluster_version = var.cluster_version # tflens:track` *inside a child module* will catch a parent-side change like `local.cluster_version` being made conditional or a new variable's default flowing in — the diff climbs through the parent's `module "<name>" { cluster_version = ... }` argument and walks any locals/vars it transitively references on the parent's side. Parent-side refs appear in the diff output prefixed with `parent.` so reviewers can tell which side of the boundary moved.

### Effective-value awareness

When the literal text of an expression changes but it evaluates to the same constant (e.g. `"1.34"` on the old side vs `var.upgrade ? "1.35" : "1.34"` with `var.upgrade = false` on the new side, both yielding `"1.34"`), the diff suppresses the value-change detail. The marker still surfaces *what's new* (a freshly-referenced variable, for example) as Informational supporting context — useful for reviewers to know what's wired in — but won't gate CI as Breaking unless the effective value actually moved.

Evaluation goes through known variable defaults and local values via the cty stdlib plus a curated set of ~46 Terraform built-ins (`length`, `contains`, `merge`, `lookup`, `concat`, `toset`, `lower`, `format`, `replace`, `sort`, `coalesce`, `min`/`max`, … — see [main README — Static evaluation surface](../../README.md#static-evaluation-surface) for the full list and what's deliberately out). Expressions that can't be evaluated statically — references to `data.X.Y` data sources, computed resource attributes (`aws_vpc.main.id`), or any non-curated function — fall back to literal text comparison. That fallback is conservative: if the texts differ, the diff is reported as Breaking even when the *real* value might be unchanged, because tflens can't prove either way without resolving the unevaluable bits.

## Where it works

- **Root module** — annotated attributes in any `.tf` file at the project root are diffed against the same path at the base ref.
- **Child modules** — every module call (recursively) is also covered, regardless of source type. Local-source children get tracked diffing in addition to consumption checks; registry/git children get it in addition to the full API diff.

Tracked-attribute diffs always count toward the `tflens diff` exit code, so CI gates work without extra wiring.

## Related

- **[`docs/commands/diff.md`](diff.md)** — the command that surfaces tracked-attribute changes.
- **[`docs/commands/diff.md#plan-enrichment`](diff.md#plan-enrichment)** — the consumer-side counterpart for when a plan is available.
- **[main README — Static evaluation surface](../../README.md#static-evaluation-surface)** — the curated stdlib functions used for effective-value collapse.
