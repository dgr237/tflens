# Generating Crossplane Composition + XRD from `tflens export`

This is a worked **prototype** showing how to consume the JSON document produced by `tflens export` and emit a Crossplane [`Composition`](https://docs.crossplane.io/latest/concepts/compositions/) and [`CompositeResourceDefinition`](https://docs.crossplane.io/latest/concepts/composite-resource-definitions/) (XRD) targeting the [Upbound provider-aws](https://marketplace.upbound.io/providers/upbound/provider-aws) managed resources.

It's a sibling of the [kro POC](../export-to-kro-rgd/) — same input fixture (an EKS cluster + IAM role), different output target. Comparing the two shows that the export's `{text, value?, ast?}` shape is portable across substantially different translation models, not just kro-shaped.

> **Status: prototype.** Both the `tflens export` schema (`0.3.0-prototype`) and this generator are scaffolding. Field renames and shape changes are expected.

## Quick start

```bash
# From the repo root:
go build -o tflens
./tflens export docs/export-to-crossplane/fixture | \
    python3 docs/export-to-crossplane/generator.py > xrd-and-composition.yaml
```

The committed `sample-output.yaml` is what that command produces against the bundled fixture — a multi-document YAML file with the XRD first and the Composition second, separated by `---`.

## Files

| File | Purpose |
| --- | --- |
| `fixture/main.tf` | Small Terraform input (EKS cluster + IAM role + cross-resource ARN ref + `format()`-based naming + variable defaults + an output). Same as the kro POC's fixture minus the dynamic block — Crossplane classic Compositions can't iterate without Composition Functions, which this POC doesn't cover. |
| `generator.py` | Stdlib-only Python that walks the export JSON and emits XRD + Composition on stdout |
| `sample-output.yaml` | Reference output — what the generator produces against the bundled fixture today |

## Translation model

| Terraform | Crossplane |
| --- | --- |
| `variable "X"` | XRD `spec.versions[].schema.openAPIV3Schema.properties.spec.properties.parameters.properties.X` (with type + default + required when no default) |
| `resource "T" "N" { ... }` | Composition `spec.resources[]` entry with `name: N` and a `base:` template (the Upbound managed resource) |
| Static literal attribute | Set directly in `base.spec.forProvider.<attr>` |
| `var.X` reference | `patches: - fromFieldPath: spec.parameters.X, toFieldPath: spec.forProvider.<attr>` |
| `format("%s-y", var.X)` | `patches: - fromFieldPath: spec.parameters.X, toFieldPath: ..., transforms: [{ type: string, string: { fmt: "%s-y" } }]` |
| `<resource_type>.foo.<attr>` (cross-resource ref) | Marked as a `# TODO` patch — Crossplane has several ref mechanisms (named `ResourceRef`, `MatchControllerRef`, external-name annotations); the converter author picks per scenario rather than guessing |
| snake_case attribute name | camelCase (Upbound convention; some attrs renamed via the per-resource `attr_renames` table) |
| `nested_block { ... }` | `spec.forProvider.<renamed>: { ... }` (recursive) |
| `dynamic "name" { ... }` | Marked as a `# TODO` — see [Dynamic blocks](#dynamic-blocks) |
| `output "X" { value = ... }` | Not currently emitted — Crossplane status sync would need the consuming MRs' `ToCompositeFieldPath` patches; future POC work |

## Crossplane vs kro: how the same export becomes two different documents

Same input. Side-by-side translation differences:

| Aspect | kro RGD | Crossplane Composition |
| --- | --- | --- |
| Variable refs | `${schema.spec.X}` (CEL substitution into the value at apply time) | Patch with `fromFieldPath: spec.parameters.X` (declarative wiring at compile time) |
| Cross-resource refs | `${resources.foo.status.ackResourceMetadata.arn}` (CEL knows the dependency from the ref itself) | Explicit `ResourceRef` with a name selector OR external-name patch (no implicit dependency tracking) |
| Function calls | CEL translation (`format` → `+`, `jsonencode` → `json.marshal`, etc.) | `transforms` array on the patch (string fmt, math, map, convert, …); function calls that don't fit the small `transforms` vocabulary need a Composition Function |
| Iteration (`dynamic`) | CEL `.map(item, {...})` inline | Composition Function (function-go-templating, function-patch-and-transform with array indices) — out of scope for classic Compositions |
| Inline literal values | Native YAML in the resource spec | Native YAML in the resource `base.spec.forProvider` |
| Static `jsonencode({...})` | Literal JSON string in the YAML (cty stdlib evaluates it at export time) | Same — literal JSON string in the resource base |
| Provider model | ACK CRDs (`<service>.services.k8s.aws/v1alpha1`) | Upbound provider-aws CRDs (`<service>.aws.upbound.io/v1beta1`) — auto-generated from the Terraform provider, so attribute names are the camelCased Terraform names rather than ACK's AWS-API spellings |

The export's three complementary fields each pull their weight differently:

- **`value`** — used identically by both: literal-evaluable expressions become native YAML values directly, no CEL/patch needed.
- **`ast`** — used identically for translating function calls (`format`, `jsonencode`, etc.), but the *output* differs (CEL string for kro, transforms array for Crossplane).
- **`text`** — used identically as the fallback / commented-out reference for the `# TODO` cases.

## Subtleties the POC handles

### Patch path generation

Each managed-resource attribute gets a `toFieldPath` like `spec.forProvider.<camelCased>`; nested blocks push a path prefix (`spec.forProvider.vpcConfig.subnetIds`). Repeated nested blocks would index (`spec.forProvider.ebsBlockDevices[0].volumeSize`) but our fixture doesn't exercise that — the kro POC's `nested_blocks_eks` golden has the equivalent shape if you want to see how a converter would walk it.

### `format()` → string-transform patch

`name = format("%s-eks-role", var.cluster_name)` becomes a single patch with a `transforms: [{type: string, string: {fmt: "%s-eks-role"}}]` entry — no Composition Function needed, since Crossplane's built-in transforms cover printf-style formatting natively. Multi-arg `format` (`format("%s-%d", ...)`) needs a Composition Function — flagged as TODO by the POC.

### Static `jsonencode({...})` round-trip

Identical to the kro case: when the JSON body has no variable/resource refs, the cty stdlib evaluates the whole `jsonencode(...)` at export time and the result is a literal JSON string. Crossplane sees `assumeRolePolicy: "{\"Statement\":[...]}"` directly — no patch needed.

### Cross-resource refs are deliberately TODO'd

`role_arn = aws_iam_role.cluster.arn` could mean any of:

- A named `ResourceRef` on the EKS Cluster MR pointing at the Role MR's external-name
- A `MatchControllerRef` selector picking up any Role with the same composite owner
- An external-name annotation pre-set on the Role and patched onto the Cluster

These have different semantics (eager binding vs late binding, single match vs multi, etc.). Picking the wrong one silently produces working-looking-but-wrong output. The POC emits a TODO with the original Terraform expression and a placeholder patch source so the YAML is valid but the converter author has to make the choice deliberately.

### Dynamic blocks

Classic Compositions don't have an iteration primitive. The two production approaches:

1. **`function-go-templating`** — Helm-style `{{ range }}` over the source list, generating one nested-block YAML chunk per element. Gives the most flexibility but needs the Function chain configured.
2. **`function-patch-and-transform` with array-indexed patches** — pre-allocate N slots in the base, patch into each by index. Bounded by the `for_each` source size at compile time.

The POC marks dynamic blocks as TODO rather than picking one. The export captures everything the converter needs (`for_each` source, iterator name, content body with iterator-rewritten refs) — see the [kro POC's `emit_dynamic` helper](../export-to-kro-rgd/generator.py) for a worked iterator-rewriting walker that would adapt cleanly to either Crossplane Function approach.

## Known gaps surfaced by the POC

| Limitation | Workaround for now |
| --- | --- |
| Cross-resource refs emit as `# TODO` | Pick a Crossplane reference style per case; the export gives you the original ref text and the AST so you know which resource is being referenced. |
| `dynamic` blocks not translated | Use a Composition Function (`function-go-templating` or `function-patch-and-transform` with array-indexed patches). The export's `dynamic_blocks` field carries everything needed. |
| `output { ... }` blocks not emitted | Crossplane status sync needs `ToCompositeFieldPath` patches on the consuming MR — not just from the XR side. Future POC. |
| Multi-arg `format("%s-%d", a, b)` | Crossplane's `string` transform supports single-arg `fmt` cleanly. Multi-arg needs a Composition Function (typically `function-go-templating`). |
| Many AWS resource types lack Upbound CRDs | Per-resource manual mapping in `UPBOUND_MAPPING`. The [Upbound provider-aws marketplace listing](https://marketplace.upbound.io/providers/upbound/provider-aws) lists every CRD they ship; a production converter would generate the mapping table from there. |
| No XR `compositionRef` / `compositionSelector` | The POC emits the Composition with a fixed `compositeTypeRef`; production converters would let users wire claims to compositions however suits them. |

## Effort estimate

The export gives you the parser/analyser/evaluator/dependency-graph layers — usually the hardest 60% of building a converter. What's left for a Crossplane production target is:

- **Patch-path emitter** (~150-300 LOC). The POC's `expr_to_patch_or_literal` is the template — extend it for the `transforms` vocabulary you want to support (math, map, convert, etc.).
- **Cross-resource ref policy** (~100 LOC + per-converter taste decisions). Decide whether to default to `ResourceRef` by name or `MatchControllerRef` by label, with overrides.
- **Composition Function integration** for dynamic blocks (significant — varies per Function choice).
- **Type/attribute mapping table** for Upbound's CRDs. The POC has hand-curated entries for two resource types; a production converter would generate the table from Upbound's CRD catalogue.

Adding new Terraform resource types to this generator is mechanical: extend `UPBOUND_MAPPING` with the resource's `apiVersion`, `kind`, and per-attribute renames. Extending the patch translator for new function calls is local to `expr_to_patch_or_literal`.

## Why a separate POC vs extending the kro one

To stress-test the export schema. If we'd only built the kro converter, the schema would have evolved to be implicitly kro-shaped — anywhere CEL idioms guided a design decision, the schema would have absorbed those choices. Producing a second target with a substantially different translation model (declarative patches vs CEL substitution; explicit cross-resource-ref policy vs ref-via-traversal; transforms vocabulary vs general-purpose CEL) proves the schema's portability and surfaces design choices that would have stayed hidden otherwise.

If you're building a third-target converter (Pulumi, CDK for Terraform, Helm, …) and the export turns out to be a poor fit, please open an issue — the shape conversation is exactly what the experimental phase exists for.
