# `tflens export` (experimental)

Walks the project tree (root + all resolvable child modules) and emits a single JSON document containing the enriched entity model that tflens has built up internally — variables / outputs / resources with parsed type info, evaluated values where statically resolvable, dependency graph, tracked-attribute markers.

> **Status: prototype.** The output shape is explicitly versioned (`schema_version` field) and flagged `_experimental: true` in every emitted document. **Do not depend on field stability across minor versions until this graduates** — entries may be added, renamed, or restructured in response to feedback from downstream converter authors.

## When to use it

- **Building a converter** that translates Terraform configurations to other provisioning systems (kro, Crossplane, Pulumi, CDK for Terraform, …) — `export` gives you parsed types + cross-module resolution + evaluated values without re-implementing the parser/analyser/resolver layers.
- **Custom analysis tooling** — pipe the JSON into your own scripts for compliance checks, dependency-graph visualisation, or whatever else the structured shape unlocks.

If you just want to know what changed in your module's API between two refs, use [`diff`](diff.md), not `export`.

## Usage

```
tflens export [path]
```

`path` defaults to cwd. Output is JSON on stdout.

## Quick example

```hcl
# main.tf
variable "region" {
  type    = string
  default = "us-east-1"
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags = {
    Name = "main"
    Env  = "prod"
  }
}
```

```bash
tflens export .
```

```json
{
  "schema_version": "0.4.0-prototype",
  "_experimental": true,
  "root": {
    "module": {
      "variables": [
        {
          "name": "region",
          "type": "string",
          "default": { "text": "\"us-east-1\"", "value": "us-east-1" }
        }
      ],
      "resources": [
        {
          "type": "aws_vpc",
          "name": "main",
          "attributes": {
            "cidr_block": { "text": "\"10.0.0.0/16\"", "value": "10.0.0.0/16" },
            "tags": {
              "text": "{ Name = \"main\", Env = \"prod\" }",
              "value": { "Name": "main", "Env": "prod" },
              "ast": { "node": "object_cons", "items": [...] }
            }
          }
        }
      ]
    }
  }
}
```

(Truncated for brevity. The real output is more verbose — see [Worked example](#worked-example) for a complete document.)

## What it emits

- **Unified expression shape.** Every field that holds an HCL expression — resource attributes, `count`, `for_each`, `depends_on`, lifecycle (`ignore_changes`, `replace_triggered_by`), module-call arguments, locals, outputs, variable defaults — emits as `{text, value?, ast?}`:
  - `text` — the canonical source bytes
  - `value` — the cty-marshalled JSON when the curated stdlib resolves the expression
  - `ast` — the structural decomposition as a tagged JSON tree (`function_call`, `scope_traversal`, `binary_op`, `conditional`, `for`, `splat`, `object_cons`, …) so non-Go consumers can translate expressions without re-parsing the text

  Tracked-attribute records (`# tflens:track` markers) emit only `expression_text` — they're diff-machinery, not converter input, so the text-only shape is appropriate.

- **Per module:**
  - **Variables** — parsed type + default expression + sensitivity flags + structured `validations` list (each with `condition` and optional `error_message` as expressions).
  - **Outputs** — value expression + sensitivity + `preconditions` / `postconditions` lists.
  - **Resources + data sources** — every meta-arg + per-attribute map + recursive `blocks` map for nested blocks like EKS's `vpc_config { ... }` / `encryption_config { provider { key_arn = ... } }`. Repeated blocks (`ingress { ... } × N`) come back as a list of instances in source order. **`dynamic_blocks` map** alongside `blocks` for `dynamic "name" { for_each = ..., iterator = ..., content { ... } }` constructs — for_each, iterator name, and recursive content body all surfaced. **`preconditions` / `postconditions` lists** with full condition + error_message expressions.
  - **Locals** — value expression with `evaluated_value` when the curated stdlib resolves it.
  - **Module calls** — source, version, count/for_each, full argument map.

- **The `terraform { }` block** — required version, required providers, backend type, **`providers` list of every top-level `provider "X" { alias = "...", ... }` instance with its per-attribute config**.

- **Dependency graph** — adjacency map of canonical entity IDs.

- **Project tree** — child modules nested under `root.children.<call-name>` recursively, with the original `source` string preserved.

Evaluated values come through the curated stdlib (see [main README — Static evaluation surface](../../README.md#static-evaluation-surface)) — anything that resolves statically (literals, variable defaults, `format`/`jsonencode`/`lower`/etc. of known constants, transitive var/local refs) gets an `evaluated_value` populated with both the cty type and the JSON value. Anything that reaches a computed attribute, data source, or non-curated function omits `evaluated_value` and surfaces only `value_text` — converters can choose what to do with unevaluable expressions.

## Why it exists

Downstream tools that translate Terraform configurations into other provisioning systems all need the same upstream work: parse the HCL, infer types, resolve cross-module references, evaluate what's statically resolvable, build the dependency graph. tflens has all of that already. `export` makes it accessible without each converter re-implementing the parser/analyser layers.

`hashicorp/hcl`'s parser output gives you the raw AST — fine if you want literal source bytes, but you'd still need to do the type inference and cross-module work yourself. `terraform show -json` gives you the fully-evaluated plan — but it requires provider credentials, real state, and provider schemas, which static converters don't want to deal with. `tflens export` sits between: schema-free, providerless, no plan required, but with the type and dependency information that raw HCL doesn't surface.

## Worked example

Three end-to-end POCs consume the export JSON and emit different target shapes. The orthogonality of orchestration layer × managed-resource provider is deliberate — picking different combinations validates that the export schema isn't implicitly shaped to any one target:

| POC | Orchestration | Managed Resources |
| --- | --- | --- |
| [`docs/export-to-kro-rgd/`](../export-to-kro-rgd/) | kro RGD | ACK (`<service>.services.k8s.aws/v1alpha1`) |
| [`docs/export-to-crossplane/`](../export-to-crossplane/) | Crossplane Composition + XRD | Upbound provider-aws (`<service>.aws.upbound.io/v1beta1`) |
| [`docs/export-to-kro-crossplane/`](../export-to-kro-crossplane/) | kro RGD | Upbound provider-aws |

Comparing them shows the same export JSON producing very different shapes:

- Variable refs become **CEL `${schema.spec.X}` substitutions** (kro POCs) or **declarative `patches` with `fromFieldPath`** (Crossplane POC).
- Cross-resource refs become **`${resources.foo.status.<convention>.arn}` traversals** (kro) or **explicit `ResourceRef` / `MatchControllerRef` policy** (Crossplane).
- `format()` calls become **CEL string concat** (kro) or a **`transforms: [{type: string, ...}]` patch entry** (Crossplane).
- Dynamic blocks become **CEL `.map()` inline** (kro) or **Composition Functions** (Crossplane, TODO).
- ARN paths follow the target's convention: ACK puts it at `status.ackResourceMetadata.arn`, Upbound at `status.atProvider.arn`.

The two kro POCs (ACK vs Upbound) are 95% identical — ~50 LOC of mapping-table and constant deltas separate them. That isolation is the schema's job, and the existence of three POCs validates it works as intended. Each bundled README documents the translation model, subtleties, and effort estimates for productionisation.

## Shape stability

The shape is versioned via the `schema_version` field. While `_experimental: true`, fields may be added, renamed, or restructured between minor releases. We bump `schema_version` whenever the shape changes (even additions) so consumers can detect drift cheaply. When the prototype graduates, `_experimental` flips to `false` and the schema becomes part of the stable API contract under SemVer.

## Not yet emitted

Adding fields is cheap; reverting them after they ship is expensive — so a couple of items are still deferred until a converter author actually needs them:

- **`lifecycle`** is deliberately not in either `blocks` or `dynamic_blocks`. Its meta-arg attributes are already projected into dedicated parent-resource fields (`prevent_destroy`, `create_before_destroy`, `ignore_changes`, `replace_triggered_by`) — surfacing the block again would just duplicate. Lifecycle's nested `precondition` / `postcondition` blocks ARE surfaced, just under the parent resource's `preconditions` / `postconditions` field rather than nested inside a `lifecycle` block.

If you're building a converter and need a different shape, please open an issue — the shape conversation is exactly what the experimental phase exists for.

## Related

- **[`docs/export-to-kro-rgd/`](../export-to-kro-rgd/)** — first worked-example POC (kro RGD + ACK).
- **[`docs/export-to-crossplane/`](../export-to-crossplane/)** — second worked-example POC (Crossplane Composition + Upbound).
- **[`docs/export-to-kro-crossplane/`](../export-to-kro-crossplane/)** — third worked-example POC (kro RGD + Upbound).
- **[main README — Static evaluation surface](../../README.md#static-evaluation-surface)** — what gets statically evaluated vs. what surfaces as text-only.
