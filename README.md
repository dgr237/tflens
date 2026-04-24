# tflens

A standalone Terraform analysis tool, written in Go. Parsing is delegated to [hashicorp/hcl/v2](https://github.com/hashicorp/hcl) (the same library Terraform itself uses); the CLI layer uses [spf13/cobra](https://github.com/spf13/cobra). The analysis, diff, constraint, and module-resolution logic is implemented directly with no further runtime dependencies.

Parses `.tf` files, builds a dependency graph, validates references and types, and diffs two module versions to surface breaking changes. Does not execute Terraform and does not need provider schemas.

Optionally fetches module sources (Terraform Registry or git) on demand so downstream analysis can traverse into them — see [Module resolution](#module-resolution). Pass `--offline` to disable network fetches; local paths and `.terraform/modules/modules.json` entries are always resolved regardless.

## Install / build

```
make build           # produces ./tflens
make install         # installs into $(go env GOPATH)/bin
make check           # vet + fmt-check + test
make test            # tests only
make coverage        # produces coverage.html
make help            # list all targets
```

Or without make:

```
go build -o tflens .
go test ./...
```

Requires Go 1.25+. For network-mode module resolution (registry / git), `git` must also be available on `$PATH`.

Every subcommand has auto-generated help:

```
tflens --help
tflens validate --help
tflens diff --help
```

Shell completion scripts (bash / zsh / fish / PowerShell) are available via
`tflens completion <shell>`.

Global flags accepted on every subcommand:

- `--format json` — emit structured output on stdout; warnings stay on stderr as plain text, so stdout stays pipeable.
- `--offline` — disable registry and git fetches; only local paths and `.terraform/modules/modules.json` entries are resolved.

```
tflens --format json validate ./my-tf | jq '.cross_module_issues[]'
tflens --offline diff --ref main ./my-tf
```

## Commands

| Command | Purpose |
| --- | --- |
| `inventory <path>` | List all declared entities (variables, locals, resources, data sources, modules, outputs) with source locations |
| `deps <path> <id>` | Show what an entity depends on and what depends on it |
| `impact <path> <id>` | Show every entity transitively affected if `<id>` changes, in topological order |
| `unused <path>` | Report entities nothing else in the module references |
| `cycles <path>` | Detect and print dependency cycles; exits non-zero if any found |
| `graph <path>` | Emit the dependency graph in Graphviz DOT format |
| `fmt <file.tf>` | Print normalised HCL; `-w` rewrites in place, `--check` exits 1 when unformatted |
| `validate <path>` | Report undefined references, type errors, `for_each`/`count` misuse, and sensitive-value leaks |
| `diff [path]` | What changed in module APIs in `path` (default cwd) vs `--ref` (default `auto`)? Behaviour depends on the child's `source`: **local children** (`./…`, `../…`) are evaluated against your parent's actual usage (only consumption errors surface as Breaking — no API noise); **registry/git children** report the full API diff (publisher's release contract). Authors can also opt specific resource attributes into the diff with `# tflens:track` markers (engine versions, instance classes, …). |
| `whatif [path] [name]` | Like `diff` but **always** consumer-view, regardless of source type. Cross-validates the parent's argument set and output references against the candidate child; only flags changes that actually affect this caller. Use this to gate any external-module upgrade. Optional `name` scopes to one module call. |
| `statediff [path] [--state file]` | Static hazard detector: resource adds/removes vs `--ref` (default `auto`), plus locals whose value changed and whose dep chain reaches `count`/`for_each`. With `--state`, lists the state instances that may be affected |
| `cache info` | Show the cache location, entry count, and total size |
| `cache clear` | Delete every cached module |

`<path>` is either a single `.tf` file or a directory (in which case all `.tf` files in it are merged into a single module view, matching Terraform's own behaviour).

## Parsing coverage

Parsing is delegated to `hashicorp/hcl/v2` (`hclparse` + `hclsyntax`), so the full Terraform HCL2 surface is supported, including:

- Blocks with labels (`resource "aws_vpc" "main" { ... }`)
- Attributes and nested blocks, including `dynamic` blocks
- All primitive literals (string, number, bool, null), heredocs
- Template strings with `${...}` interpolation and `$$` literal-dollar escapes
- Expressions: unary/binary operators with correct precedence, ternary `?:`, splat (`.*`, `[*]`), indexing, dot traversal
- Collections: tuples, objects with `=` or `:` separators
- `for` expressions for both lists and maps, with optional `if` clause
- Function calls (with `...` argument spread)

Parse errors are reported with position information (`file:line:column`). The `fmt` command runs `hclwrite.Format`, which preserves comments and blank lines.

## Validation (`validate`)

### What it catches

**Undefined references:**
- `var.X` referencing an undeclared variable
- `local.X` referencing an undeclared local
- `module.X` referencing an undeclared module call
- `data.X.Y` referencing an undeclared data source

**Type errors on variable defaults:**
- `variable "x" { type = number, default = "three" }` — default type mismatches declared type
- Object field types, including `optional(T)` wrapping, are compared structurally
- `any` accepts everything; `null` defaults are always permitted

**Meta-argument misuse:**
- `for_each` must be a map, set, or object literal; passing a list (including via `keys()`, `values()`, `concat()`, etc.) is flagged
- `count` must not be a list/set/map/object/bool literal
- Variable references in these positions are resolved to their declared types when available

**Builtin function return types:**
- A registry of roughly 80 common functions (string, numeric, collection, encoding, hash, networking, file/path, predicate) feeds the type inference. `for_each = keys(var.tags)` is flagged because `keys` returns a list.

**Sensitive propagation:**
- An output whose `value` references a `sensitive = true` variable but is not itself marked `sensitive` — Terraform rejects this at plan time.

**Cross-module inputs (when target is a directory):**
- A `module "x" { source = "..." ... }` call that omits a required child variable (one with no `default`) is flagged.
- An argument passed via the module block that has no matching variable in the child is flagged as an unknown argument.
- When the argument's type is inferable (literal, built-in function return, or `var.X` with a declared type) and the child variable has a declared type, incompatible types are flagged.
- Recursion is transitive: root → middle → leaf modules are all cross-checked.

See [Module resolution](#module-resolution) below for how `source = "..."` is turned into a directory on disk.

A broken `modules.json` is reported as a warning but does not abort the rest of the validation.

### What it does NOT catch

- **Resource-style references** (`aws_vpc.main.id`) to undeclared resources. This is deliberate: `for` expressions introduce unbound iterator variables (`item`, `v`, `k`, ...) and treating every unknown two-part reference as an undefined resource produces too many false positives without scope-aware analysis.
- **Function argument types.** Only return types are modelled. Passing the wrong type *into* a function is not flagged (Terraform will catch it at plan time with higher fidelity than we could).
- **Provider attribute types.** The shape of `aws_vpc.main.cidr_block` depends on the AWS provider schema, which this tool does not embed.
- **Condition semantics.** `validation { condition = ... }`, `precondition`, and `postcondition` block contents are not evaluated. Their presence is recorded but the conditions themselves are opaque.
- **`count` with string literals.** Terraform silently coerces `"3"` to `3`, so we allow it.
- **Cross-module validation in `--offline` mode for unresolved remote sources.** When `--offline` is set, parent → child checks require the child's directory to be resolvable — either via a local path (`./x`, `../y`) or via the post-`terraform init` manifest at `.terraform/modules/modules.json`. Registry and git sources cannot be loaded in that mode and are silently skipped. Either run `terraform init` first or drop `--offline`.
- **Cross-module validation where argument types are opaque.** A parent passing `aws_vpc.main.cidr_block` to a typed child variable produces no type-mismatch error because the resource attribute's type cannot be resolved without provider schemas.
- **Runtime values.** Defaults that call `timestamp()`, `uuid()`, or similar are not evaluated.
- **Prerelease/build metadata in semver.** Stripped during parsing.

## Diff (`diff`)

```
tflens diff [path] [--ref <base>]
```

`path` defaults to cwd; `--ref` defaults to `auto` (resolves to `@{upstream}` → `origin/HEAD` → `main` → `master`). The command diffs the **root module** (the directory at `path`) and pairs every child module call between the two trees by dotted key (e.g. `vpc.sg`).

The root module gets a full API diff — adding a required variable, removing an output, changing the backend, etc. all show up under a `Root module:` section. The operator running `terraform plan` against this directory IS the consumer, even though no parent calls the root.

For child module calls, the classification depends on **how the child is sourced**:

- **Local children** (`source = "./…"` or `"../…"`) — internal to this repo. Their API is implementation detail; only the parent's actual consumption is observable. `diff` runs cross-validation of the new parent against the new child and reports a Breaking change only when the parent's usage is broken (passes an unknown arg, fails to pass a now-required input, or references a removed `module.<name>.<output>`). Renaming a variable that the parent updated atomically is silent.
- **Registry / git children** — published by someone else (or by you, in a release). The publisher owns breaking-change discipline. `diff` reports the full API diff (every variable / output / type / lifecycle change) classified as Breaking, NonBreaking, or Informational. A removed variable shows up regardless of whether your specific parent passed it.

Both modes exit non-zero when any Breaking changes exist (suitable for CI gating).

Classifications used for registry/git children:

- **Breaking** — existing callers or state will be affected
- **NonBreaking** — safe to upgrade through
- **Informational** — operational or cosmetic, but worth surfacing

### Fix hints

Most breaking changes carry a one-line `hint:` with the conventional fix. Example:

```
Module "vpc": (content changed)
  Breaking (1):
    variable.region: required variable added (no default)
      hint: add `default = ...` to make it optional, or document that callers must set it
```

Hints cover the common cases: required-variable-added (suggest `default`), required-object-field-added (suggest `optional()`), resource removed/renamed (suggest `removed {}` / `moved {}` blocks with the exact entity IDs filled in), backend changes (`terraform init -migrate-state`), `count`↔`for_each` transitions, sensitive-leak removals on outputs, and the four cross-validate consumption errors. The JSON output emits the same string under a `"hint"` key (omitted when empty).

### What it catches

**Variables:**

| Change | Kind |
| --- | --- |
| Variable removed | Breaking |
| New required variable (no default) | Breaking |
| New optional variable (has default) | NonBreaking |
| `default` removed (optional → required) | Breaking |
| `default` added (required → optional) | NonBreaking |
| Type widened — every old value is still acceptable (`string` → `any`, `list(string)` → `list(any)`, `map(T)` → `map(any)`, …; backed by `cty.Convert`) | NonBreaking |
| Type narrowed — some old values are now rejected (`any` → `string`, `list(any)` → `list(string)`, …) | Breaking |
| Type changed and incompatible (unrelated shapes) | Breaking |
| Existing default still converts cleanly to the new type (emitted alongside the type change so callers using the default see they're unaffected) | Informational |
| Object field added (required) | Breaking |
| Object field added (optional) | NonBreaking |
| Object field removed | Breaking |
| Object field optional → required | Breaking |
| Object field required → optional | NonBreaking |
| Object field inner type widened (e.g. `object({a=string})` → `object({a=any})`) | NonBreaking |
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
| Output value type narrowed or incompatible (inferred via `var.X` → declared type, function-return tables, or constant evaluation; e.g. `string` → `list(string)` from `[for ...]`) | Breaking |
| `value` expression changed but inferred type compatible (or types unknown) | Informational |
| Referenced `local` expression changed (indirect, surfaced when the output expression text is unchanged) | Informational |
| `precondition` / `postcondition` block added or removed (compared by canonical condition text — reordering is a no-op) | Informational |
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
| `for_each` *key type* narrowed or incompatible (e.g. `set(string)` → `set(number)` — every instance is re-addressed under a different key; also fires when the for_each text is unchanged but a referenced variable's type narrowed underneath) | Breaking |
| `for_each` *expression* changed but key type compatible (or unknown) | Informational |
| `count` *expression* infers as a non-number type (e.g. `var.n` retyped from `number` to `list(string)`) — Terraform will reject the plan | Breaking |
| `count` *expression* changed but stays numeric (or type unknown) | Informational |
| `provider = aws.east` → `aws.west` | Breaking |
| Lifecycle: `prevent_destroy` added/removed | Informational |
| Lifecycle: `create_before_destroy` toggled | Informational |
| Lifecycle: `ignore_changes = all` narrowed to a list (drift detection now fires on attributes that were previously suppressed) | Breaking |
| Lifecycle: `ignore_changes` widened to `all` (drift detection now suppressed for every attribute) | NonBreaking |
| Lifecycle: `ignore_changes` / `replace_triggered_by` content changed (otherwise) | Informational |
| Lifecycle: `precondition` / `postcondition` block added or removed (compared by canonical condition text — reordering is a no-op) | Informational |
| `depends_on` changed | Informational |
| Module `source` changed | Informational |
| Module `version` constraint changed (semver-aware, below) | Breaking / NonBreaking / Informational |
| Module argument (non-meta-arg attribute) added/removed/value-changed | Informational |

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
| `backend` config attribute added, removed, or value changed (state may relocate) | Breaking |

**Semver-aware version constraint comparison:**

Version constraints (`>= 1.0`, `~> 4.0`, `!= 1.2.3`, compound forms like `">= 1.0, < 2.0"`) are parsed into interval sets and compared:

- **Equal** — same satisfying set
- **Broadened** (old ⊂ new) — NonBreaking
- **Narrowed** (new ⊂ old) — Breaking ("tightened")
- **Overlap** (neither subset, some overlap) — Breaking ("partially narrowed")
- **Disjoint** — Breaking ("incompatible")
- **Unparseable** — falls back to Informational with a generic detail

### What it does NOT catch

- **Resource attribute body changes.** A resource gaining or changing an attribute (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) is not diffed by default. The noise-to-signal ratio of a full body diff is too low; if the resource is removed, renamed, or has its meta-arguments changed, we flag that. Authors can opt specific attributes into the diff with the [`# tflens:track`](#tracked-attributes-for-application-development-teams) marker — used for engine versions, instance classes, and **force-new attributes** (`cluster_name`, `identifier`, …) where a change quietly forces a destroy and recreate.
- **Resource provider schema changes.** We cannot tell that an AWS provider bump from v4 to v5 silently changed `aws_vpc.main.cidr_block` from a string to a list, because we do not embed provider schemas.
- **Dynamic block content.** `dynamic "ingress" { for_each = ... }` bodies generate blocks at plan time. Without evaluating the `for_each`, the generated block set is opaque.
- **Condition strictness.** If `validation { condition = length(var.x) > 0 }` becomes `condition = length(var.x) > 5`, we record that a validation block exists in both versions but cannot tell that the new condition rejects strictly more inputs.
- **Default value *content* changes.** Only the presence or absence of a default is diffed by default. Changing `default = "dev"` to `default = "prod"` produces no change — most real modules change defaults deliberately, and flagging every edit produces too much noise. When the variable is referenced from a tracked attribute (`# tflens:track`), the default is followed and changes to it ARE flagged against the tracked attribute.
- **Description / documentation changes.** Informational-only, currently skipped.
- **Ambiguous renames.** The rename heuristic pairs exactly one removed entity with one added entity of the same kind and type. When there are multiple candidates (two removed, two added of the same type), no pairing is attempted — each is reported individually.
- **Type coercion subtleties.** `list(string)` → `set(string)` is flagged as a type change. Terraform auto-converts in some expression contexts but not others (index access `[0]` fails on sets); distinguishing safe from unsafe coercions requires knowing how each caller uses the variable, which is cross-module.
- **Type narrowing of custom objects without `optional()`.** Adding a field to an object type is correctly flagged as breaking, but this tool cannot reason about *what* the provider would accept for that field's value.
- **`check { assert { ... } }` blocks** (Terraform 1.5+) — not currently parsed.
- **`import { ... }` blocks** (Terraform 1.5+) — not currently parsed.
- **Provisioner blocks** (`provisioner "local-exec"`, `connection`) — not currently parsed; their presence or absence affects teardown and creation but is not flagged.
- **Nested moved-block expressions with indices.** `moved { from = aws_vpc.main[0], to = aws_vpc.main["a"] }` is not recognised; only bare resource references in `from` / `to` are parsed.
- **Children that cannot be resolved offline.** `diff`, `whatif`, and `statediff` only compare children that both resolvers can materialise. In `--offline` mode or against registry/git sources missing from the cache and from `.terraform/modules/modules.json`, the child is skipped rather than reported.

## What-if upgrade analysis (`whatif`)

`whatif` is the **consumer view**: it answers *if I merged the working tree's module changes, would my parent still work?*

```
tflens whatif [path] [module-call-name] [--ref <base>]
```

For every module call in `path` (default cwd) that differs between the working tree and the base ref (default `auto` → `@{upstream}` → `origin/HEAD` → `main` → `master`), `whatif` loads the parent at base, loads the candidate child from the working tree, and reports:

1. **Direct impact:** cross-validation of the parent's `module "<name>" { ... }` block against the candidate — missing required inputs, unknown arguments, and type mismatches the upgrade would introduce.
2. **Module API changes:** the full `diff` between the base and working-tree child, for context.

With an optional `module-call-name`, scope to one call. Exits non-zero when the direct-impact list is non-empty (suitable for CI gating).

This is strictly more focused than `diff`. A child module can ship many "Breaking" API changes that don't affect a particular caller — e.g. removing a variable the parent never passed, or tightening a type the parent's value already satisfies. `whatif` cross-validates the parent's argument set against the candidate child's variables and only flags changes that *actually break this caller*.

### `whatif` vs `diff`

Both compare the path against a git base. After the source-type rules above, they overlap on local-source children — both ask "does the parent's usage still work?". The difference is on **registry / git children**:

| Child source | `diff` | `whatif` |
|---|---|---|
| Local (`./…`, `../…`) | Consumer view (cross-validate parent vs new child) | Consumer view (same) |
| Registry / git | Author view (full API diff classified by API-shape rules) | Consumer view (cross-validate parent vs new child; suppresses changes that don't affect this caller) |

In CI:

- `tflens diff` for the broad question "anything changed I should know about?" — quiet on local internals you've updated atomically, loud on registry-module API drift.
- `tflens whatif` when you want to gate a PR strictly on "will this break MY usage?" — quiet on every module-call upgrade that your parent absorbs cleanly, regardless of how the child is sourced.

## State-level hazard detection (`statediff`)

```
tflens statediff [path] [--ref <base>] [--state state.json]
```

`path` defaults to cwd; `--ref` defaults to `auto`.

A static hazard detector for PRs that may unintentionally add, destroy, or re-instance resources. It answers: *if I merge this branch, which of my state's resource instances are at risk?*

It reports:

- **Resource identity adds and removes** — every `resource "TYPE" "NAME"` declaration that appears in one branch but not the other. A missing declaration is the most direct path to a destroy.
- **Renames via `moved {}` blocks** — a `moved { from = aws_vpc.old, to = aws_vpc.new }` block in the new tree that pairs a real removal with a real addition is recognised as a rename, listed separately, and does NOT contribute to the exit code (Terraform handles the rename without destroy/recreate).
- **Sensitive value changes** — locals OR variable defaults whose expression changed between branches, whose dependency chain reaches a `count` or `for_each` meta-argument. This is the silent class of bug: trim a list in `locals { regions = [...] }` (or drop `variable "n" { default = 3 }` to `1`) and a resource that expands from that value quietly loses instances. No attribute in the resource block itself changed — tools that only diff resource blocks miss it.
- **State cross-reference** — when `--state state.json` is given, every flagged resource is annotated with the instances currently in state. A reviewer sees concrete addresses (`aws_instance.web["us-west-2"]`) rather than abstract warnings.
- **State orphans** — addresses in state that have no corresponding declaration in the working tree. These indicate pre-existing drift and are reported separately; they do NOT contribute to the exit code since they are not caused by this PR.

Exit code is 1 when any resource add/remove or sensitive local fires.

### What it does not do

- **Plan simulation.** Attribute-level diffs (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) need provider schemas and expression evaluation — that is `terraform plan`'s job. `statediff` is a complementary, cheap, schema-free check that catches a different class of hazard than plan.
- **Full expression evaluation.** A sensitive local with `count = length(local.xs) > 0 ? 1 : 0` is flagged as "may change" without us knowing whether `length` actually crosses the boundary. The signal is a warning, not a definitive answer.
- **Variable-driven changes across module callers.** A `count = var.n` resource where the caller on one branch passes `n = 3` and the other passes `n = 1` is not currently followed across module boundaries.

## Tracked attributes (for application-development teams)

`diff` deliberately ignores most resource-block attribute changes — they're noise relative to the public API surface (variables, outputs, types). But some attributes are load-bearing for *operations*. Two broad classes are worth pulling out:

- **In-place but disruptive** — an EKS `cluster_version` bump, an RDS `engine_version` jump, an EC2 `instance_class` resize. The resource is updated in place but the change has real-world consequences (downtime, add-on compatibility, cost).
- **Force-new (destroy + recreate)** — an EKS `cluster_name`, an RDS `identifier`, an `aws_db_subnet_group.name`. Terraform's plan will show `# forces replacement` for these, but only after `terraform plan` runs against an applied state. At PR-review time the diff looks like an innocent string change. Worse, the value is usually computed (`"${var.env}-${local.suffix}"`), so the literal text in the resource block doesn't change at all when `local.suffix` flips from `"primary"` to `"secondary"`.

These are easy to merge by accident and hard to roll back. AD teams own the resource modules; they're the people who know which attributes need a second pair of eyes.

Mark them with `# tflens:track`:

```hcl
resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = "1.28" # tflens:track: bump only after add-on compatibility check
}
```

Now `tflens diff` flags any change to `cluster_version` as **Breaking**, with the comment text surfaced as the hint.

### Marker placement

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

### Indirection through variables and locals

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

#### Force-new attributes with computed values

The indirection rule extends to string interpolation, which is where force-new attributes usually hide. Consider:

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

### Why removing the marker is itself flagged

If a teammate decides to "just remove the comment" to avoid the diff, that's exactly the failure mode the marker exists to prevent. Marker removal is reported as a Breaking change of its own:

```
resource.aws_eks_cluster.this.cluster_version: tracked-attribute marker removed (the safety guard is gone)
  hint: restore the `# tflens:track` comment, or remove the attribute entirely if the resource is gone
```

Adding a new marker is reported as Informational — useful in PR review but not gating.

### Where it works

- **Root module** — annotated attributes in any `.tf` file at the project root are diffed against the same path at the base ref.
- **Child modules** — every module call (recursively) is also covered, regardless of source type. Local-source children get tracked diffing in addition to consumption checks; registry/git children get it in addition to the full API diff.

Tracked-attribute diffs always count toward the `tflens diff` exit code, so CI gates work without extra wiring.

## Module resolution

Commands that traverse a project (`validate`, `diff`, `whatif`, `statediff`) turn each `module "x" { source = "..." }` call into a directory on disk via a chain of resolvers, tried in order:

1. **Local path** — `source = "./x"` and `source = "../y"` resolve relative to the caller's directory. Always tried.
2. **`.terraform/modules/modules.json`** — if the manifest produced by `terraform init` is present, every module call is resolved through it by dotted key path (`vpc`, `vpc.sg`, etc.). Always tried.
3. **Terraform Registry** — sources of the form `ns/name/provider` or `host/ns/name/provider` (plus optional `//subdir`). Service discovery (`/.well-known/terraform.json`) → version list → `version` constraint (`~>`, `>=`, `<`, `=`, `!=`) resolved to a concrete version → tarball or git download → extracted into the cache. Skipped in `--offline` mode.
4. **Git** — `source = "git::<url>"` (HTTPS, SSH, or file:// for tests) plus the bare VCS shorthand `github.com/foo/bar`, `bitbucket.org/foo/bar`, `gitlab.com/foo/bar`, `codeberg.org/foo/bar`. Honours `?ref=` and `//subdir`. Skipped in `--offline` mode.

### Cache

Downloaded modules are stored under the OS user cache directory (e.g. `~/.cache/tflens/modules` on Linux, `%LocalAppData%\tflens\modules` on Windows). The cache is content-addressable and immutable: a given (host, path, concrete-version) tuple is only ever downloaded once. Delete the directory to force re-fetches.

### Private registries

Credentials are read from two sources, in this order:

1. **TFE tokens YAML file**, opt-in via `$TFE_TOKENS_FILE`. Some Terraform Enterprise org-management tooling ships per-organisation tokens in this format:

   ```yaml
   tokens:
     - address: tfe.example.com
       token: your-tfe-token
     - address: https://other.tfe.example.com
       token: another-tfe-token
   ```

   `address` may be a bare host, a `host:port` pair, or a full URL — only the host (with port if non-default) is matched against the outgoing request. Loading is strictly opt-in: with `$TFE_TOKENS_FILE` unset, tflens never reads from any conventional path. Set the variable to enable the source.

2. **Terraform CLI config file** (`$TF_CLI_CONFIG_FILE`, or `%APPDATA%\terraform.rc` on Windows, or `~/.terraformrc` elsewhere) — the standard Terraform format:

   ```
   credentials "app.terraform.io" {
     token = "your-api-token"
   }

   credentials "registry.example.com" {
     token = "..."
   }
   ```

When both files name the same host, the TFE-tokens entry wins (it's typically org-managed and more authoritative than a personal CLI config). Either source missing is fine; running with neither falls through to anonymous access.

Bearer tokens are sent **only** to requests whose `URL.Host` exactly matches a configured entry. This means a registry that redirects its tarball download URL at a third-party CDN (typical for GitHub-backed public modules) never receives the token.

### Version constraints

`~>`, `>=`, `<=`, `>`, `<`, `=`, `!=` are supported, comma-separated clauses are intersected. Version comparison follows SemVer 2.0.0, including prerelease precedence (§11). Prerelease matching is currently mathematical: `1.5.0-beta` satisfies `>= 1.0.0`.

## Fundamental limitations

These are not bugs but deliberate boundaries:

- **No Terraform execution.** This is a static analyser. Anything that requires planning, applying, or querying a provider is out of scope.
- **No provider schemas.** We do not embed AWS/GCP/Azure/etc. provider schemas. Resource attribute types, required-vs-optional attributes, and deprecations of resource types are invisible to us. Running `terraform validate` in addition to this tool catches those.
- **No expression evaluation.** Functions, conditionals, and references to computed attributes cannot be resolved statically. The inferred type of `aws_vpc.main.id` is `TypeUnknown`. The tracked-attribute pass does follow `var.X` / `local.X` references one or two hops deep — by comparing the canonical *text* of the referenced default or value, not by evaluating the expression.
- **Caller awareness only at module-call boundaries.** `whatif` and the local-source path of `diff` cross-validate a parent module's `module "x" { ... }` block against the candidate child's variables and outputs. Beyond that — e.g. whether some external repo that pinned to an old version still works after a registry-module change — is out of scope; we'd need to analyse those callers too.
- **Mid-expression comments are dropped.** Line and block comments at statement boundaries round-trip correctly; comments embedded inside a function call argument list split across lines, or inside a multi-line object/tuple literal, are lost by `fmt`.

## Editor integration

For HCL syntax/intellisense use HashiCorp's [`terraform-ls`](https://github.com/hashicorp/terraform-ls) with the official VS Code / IntelliJ / Neovim extensions — it ships with provider schemas (knows that `aws_vpc.cidr_block` is a string, etc.), which tflens deliberately does not embed.

tflens itself is CLI- and CI-shaped: run `validate` / `diff` / `whatif` / `statediff` / `inventory` to surface the analyses that are unique to it (cross-module input checks, semver-aware version constraint diffs, sensitive-value propagation, state-impact prediction).

## License

Apache License, Version 2.0. See [LICENSE](./LICENSE).

Copyright 2026 David Roberts.

## Architecture

Code is organised under `pkg/`:

| Package | Responsibility |
| --- | --- |
| `token` | Source-position type (thin wrapper over `hcl.Pos`) used across the API |
| `analysis` | Entity collection, dependency graph, type system, validation, graph algorithms (cycles, topo-sort, impact, unreferenced); consumes `hclsyntax.Body` from `pkg/loader` |
| `loader` | Multi-file / directory / recursive submodule loading via `hclparse`, cross-module input validation |
| `diff` | Two-module comparison with semver-aware constraint classification; expression equality goes through `hclwrite.Format` so whitespace-only edits don't show as changes |
| `constraint` | SemVer parsing and Terraform-style version constraint evaluation (`~>`, `>=`, ...) |
| `cache` | Content-addressable disk cache for downloaded module sources |
| `resolver` | Pluggable `Resolver` chain (local path, `.terraform/modules/modules.json`, Terraform Registry, git) with credential support |
| `tfstate` | Terraform state v4 JSON parser; exposes resource identity + instance keys for cross-reference |

The CLI layer lives in top-level `cmd/` (cobra), with one file per subcommand. `main.go` just calls `cmd.Execute()`.

The analysis is a three-pass design: collect entities → collect dependency edges → run type checking. The dependency graph powers `deps`, `impact`, `unused`, `cycles`, and the topological-sort output in `impact`.
