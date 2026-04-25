# Generating a kro RGD targeting Crossplane provider-aws managed resources

This is a worked **prototype** showing how to consume the JSON document produced by `tflens export` and emit a [kro](https://kro.run) `ResourceGraphDefinition` (RGD) where each resource template is a [Crossplane provider-aws](https://marketplace.upbound.io/providers/upbound/provider-aws) managed resource.

It's the **third POC** in the series. The other two are:

| POC | Orchestration | Managed Resources |
| --- | --- | --- |
| [`docs/export-to-kro-rgd/`](../export-to-kro-rgd/) | kro RGD | ACK CRDs (`<service>.services.k8s.aws/v1alpha1`) |
| [`docs/export-to-crossplane/`](../export-to-crossplane/) | Crossplane Composition + XRD | Upbound provider-aws (`<service>.aws.upbound.io/v1beta1`) |
| **this one** | **kro RGD** | **Upbound provider-aws (`<service>.aws.upbound.io/v1beta1`)** |

The orthogonality of the two axes (orchestration layer × managed-resource provider) is exactly the point: the export schema lets converter authors mix and match without re-implementing parser/analyser/evaluator/dependency-graph layers for each combination.

> **Status: prototype.** Both the `tflens export` schema (`0.3.0-prototype`) and this generator are scaffolding. Field renames and shape changes are expected.

## Why this combination

You'd choose kro + Crossplane provider-aws over kro + ACK when:

- **Coverage matters.** Upbound's provider-aws auto-generates from the Terraform AWS provider, so it covers virtually every AWS resource type at the time of writing. ACK's coverage is much narrower (intentionally — they hand-write each controller).
- **You want kro's CEL flexibility but Crossplane's provider catalogue.** kro's `.map()` / `json.marshal` / string-concat idioms are richer than Crossplane's `transforms` vocabulary, but you don't want the wider Crossplane Composition machinery.
- **You're already running provider-aws.** No need to install a second CRD set (ACK) just because you're now using kro for orchestration.

You'd choose this **over** kro + ACK when ACK doesn't have the resource. You'd choose this **over** Crossplane Compositions when you want kro's substitution model rather than declarative patches.

## Quick start

```bash
# From the repo root:
go build -o tflens
./tflens export docs/export-to-kro-crossplane/fixture | \
    python3 docs/export-to-kro-crossplane/generator.py > rgd.yaml
```

The committed `sample-output.yaml` is what that command produces against the bundled fixture.

## Files

| File | Purpose |
| --- | --- |
| `fixture/main.tf` | Same fixture as the kro+ACK POC — EKS cluster + IAM role + security group with `dynamic "ingress"`. Letting the same input flow through both POCs makes the contrast obvious. |
| `generator.py` | Stdlib-only Python that walks the export JSON and emits the RGD. ~95% identical to the kro+ACK generator — see "Deltas from the kro+ACK POC" below. |
| `sample-output.yaml` | Reference output. |

## Deltas from the kro+ACK POC

This generator is a near-fork of the kro+ACK one. The deltas are isolated:

1. **`CROSSPLANE_MAPPING`** instead of `ACK_MAPPING`. Upbound apiVersions (`eks.aws.upbound.io/v1beta1`) and Terraform-style camelCase attribute names (`roleArn` not `roleARN`; `assumeRolePolicy` not `assumeRolePolicyDocument`).

2. **`CROSSPLANE_ARN_PATH = "status.atProvider.arn"`** instead of `"status.ackResourceMetadata.arn"`. Crossplane provider-aws MRs expose every status field under `status.atProvider`, so `aws_iam_role.cluster.arn` translates to `${resources.cluster.status.atProvider.arn}`.

3. **`spec.forProvider` wrapping in `emit_resource`.** Crossplane MRs separate user-controlled fields (`spec.forProvider`) from provider-controlled status (`status.atProvider`). Everything the kro+ACK POC put directly under `spec` lands under `spec.forProvider` here. One extra dict-level wrap; no logic change.

4. **General cross-resource refs use `status.atProvider.<X>`** instead of plain `status.<X>` (the same namespacing as the ARN special-case).

That's it. The CEL emitter, AST walker, dynamic-block iterator-rewriting, format/jsonencode handling, parameterisation logic — all unchanged. Adding the third POC required diff-touching ~4 functions and ~50 lines of mapping table.

That isolation is itself a useful finding: **the export schema is structured so the per-target deltas live in a small mapping table + a couple of constants**, not in the generator's overall shape. A converter author can fork the kro+ACK POC for any other kro+X target by swapping the mapping table.

## Translation model

Same as the kro+ACK POC, with the deltas above:

| Terraform | kro / Crossplane provider-aws |
| --- | --- |
| `variable "X"` | `spec.schema.spec.X` (with type + default) |
| `resource "T" "N" { ... }` | `spec.resources[]` entry with `id: N`, `template:` an Upbound MR with fields under `spec.forProvider` |
| `var.X` reference | `${schema.spec.X}` (kro CEL into the schema input) |
| `<resource_type>.foo.arn` | `${resources.foo.status.atProvider.arn}` (Upbound convention) |
| `<resource_type>.foo.<other>` | `${resources.foo.status.atProvider.<other>}` |
| `format("%s-y", var.x)` | `${(schema.spec.x + "-y")}` (CEL string concat) |
| `length(var.xs)` | `${size(schema.spec.xs)}` |
| `lower(s)` / `upper(s)` | `${s.lowerAscii()}` / `${s.upperAscii()}` |
| `concat(a, b)` | `${a + b}` (CEL list addition) |
| `jsonencode(...)` | `${json.marshal(...)}` (kro's `json` CEL library) |
| snake_case attribute name | camelCase (Upbound's Terraform-derived spelling) |
| `nested_block { ... }` | `nestedBlock: { ... }` (recursive, under `spec.forProvider`) |
| repeated `block { ... } × N` | `block: [ {...}, {...}, ... ]` |
| `dynamic "name" { for_each = X, content { ... } }` | `name: ${X.map(item, { "k": item.field, ... })}` (kro CEL `.map()` with iterator-rewriting) |
| `output "X" { value = ... }` | `spec.schema.status.X` (best-effort) |

## What changes for converter authors compared to plain Crossplane Compositions

If you're coming from the [Crossplane Compositions POC](../export-to-crossplane/) and considering this hybrid:

| Aspect | Crossplane Composition (POC 2) | kro + provider-aws (this POC) |
| --- | --- | --- |
| Wiring | `patches: - fromFieldPath: spec.parameters.X, toFieldPath: spec.forProvider.<attr>` | `<attr>: ${schema.spec.X}` inline |
| Cross-resource refs | Named `ResourceRef` / `MatchControllerRef` / external-name (TODO) | `${resources.foo.status.atProvider.arn}` (just works) |
| Function calls | `transforms: [{type: string, string: {fmt: ...}}]` (limited vocabulary) | CEL expressions (string concat, json.marshal, .map(), etc.) |
| Iteration (`dynamic`) | Composition Functions (TODO) | `${X.map(item, {...})}` inline |
| Two-document output | Yes (XRD + Composition) | No (one RGD) |
| Operator surface | More moving parts (XRD + Composition + Provider configs) | Just the RGD (claims directly create the schema's CRD) |

The win of the hybrid is replacing Crossplane's narrower `transforms` vocabulary with kro's general-purpose CEL — the same expressions that would need a Composition Function in a pure Crossplane setup just work inline here. The cost is depending on kro alongside Crossplane, where a pure-Crossplane shop already has Compositions.

## Effort to add another kro+X POC

Trivial. The kro+ACK and kro+Crossplane generators are 95% identical. To target a different MR provider (e.g. provider-azure, provider-gcp, or a non-AWS Crossplane provider entirely):

1. Copy `generator.py` from this POC.
2. Replace `CROSSPLANE_MAPPING` with the target provider's CRD shapes.
3. Adjust `CROSSPLANE_ARN_PATH` (or its equivalent identity field) for the target's status convention.
4. Adjust `emit_resource`'s spec-wrapping if the target uses something other than `spec.forProvider` (e.g. provider-kubernetes uses `spec.manifest`).

The CEL emitter, AST walker, and dynamic-block translation carry over unchanged. That isolation is the export schema's job — and the existence of this POC validates it works as intended.

## Known gaps

Same as the other two POCs:

- **Locals (`local.foo`) emit a marker** — a real converter inlines them.
- **`count = X` not handled** — kro's iteration is per-resource via `${...}`; production code generates one resource per known value or uses a Composition-Function-equivalent pattern.
- **Splat expressions (`aws_subnet.example[*].id`)** — emit as `.map()` in CEL; approximate.
- **Many Terraform resource types absent from `CROSSPLANE_MAPPING`** — extend the table from Upbound's CRD catalogue.
