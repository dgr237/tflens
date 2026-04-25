# Generating a kro RGD + ACK custom resources from `tflens export`

This is a worked **prototype** showing how to consume the JSON document produced by `tflens export` and emit a [kro](https://kro.run) `ResourceGraphDefinition` (RGD) targeting [AWS Controllers for Kubernetes (ACK)](https://aws-controllers-k8s.github.io/community/) custom resources.

It is intentionally narrow — two Terraform resource types (`aws_iam_role`, `aws_eks_cluster`), one variable-driven parameterisation, one cross-resource ARN reference, one `format()` call, one nested block. The goal is to demonstrate that `tflens export`'s `{text, value?, ast?}` shape gives a converter author enough information to do the job without re-implementing parsing, type inference, or cross-module resolution.

> **Status: prototype.** Both the `tflens export` schema (`0.3.0-prototype`) and this generator are scaffolding. Field renames and shape changes are expected.

## Quick start

```bash
# From the repo root:
go build -o tflens
./tflens export docs/export-to-kro-rgd/fixture | \
    python3 docs/export-to-kro-rgd/generator.py > rgd.yaml
```

The committed `sample-output.yaml` is what that command produces against the bundled fixture.

## Files

| File | Purpose |
| --- | --- |
| `fixture/main.tf` | Small Terraform input (EKS cluster + IAM role + cross-resource ARN ref + `format()`-based naming + variable defaults + an output) |
| `generator.py` | Stdlib-only Python that walks the export JSON and emits an RGD on stdout |
| `sample-output.yaml` | Reference output — what the generator produces against the fixture today |

## Translation model

The generator implements roughly this mapping:

| Terraform | kro / ACK |
| --- | --- |
| `variable "X"` | `spec.schema.spec.X` (with type + default) |
| `resource "T" "N" { ... }` | `spec.resources[]` entry with `id: N` and an ACK CRD `template` |
| `var.X` reference | `${schema.spec.X}` (CEL into the schema input) |
| `<resource_type>.foo.arn` | `${resources.foo.status.ackResourceMetadata.arn}` (ACK convention — every ACK resource exposes its AWS ARN at this path) |
| `<resource_type>.foo.<other>` | `${resources.foo.status.<other>}` |
| `format("%s-y", var.x)` | `${(schema.spec.x + "-y")}` (CEL string concat) |
| `length(var.xs)` | `${size(schema.spec.xs)}` |
| `lower(s)` / `upper(s)` | `${s.lowerAscii()}` / `${s.upperAscii()}` |
| `concat(a, b)` | `${a + b}` (CEL list addition) |
| `jsonencode(...)` | `${json.marshal(...)}` (kro ships a [`json` CEL library](https://github.com/kubernetes-sigs/kro/blob/main/pkg/cel/library/json.go) — round-trips cleanly) |
| `jsondecode(...)` | `${json.unmarshal(...)}` |
| snake_case attribute name | camelCase (ACK convention; some attrs renamed via the per-resource `attr_renames` table) |
| `nested_block { ... }` | `nestedBlock: { ... }` (recursive) |
| repeated `block { ... } × N` | `block: [ {...}, {...}, ... ]` |
| `dynamic "name" { for_each = X, content { ... } }` | `name: ${X.map(item, { "k": item.field, ... })}` — CEL `.map()` over the source list with the per-iteration template inlined as a CEL object literal. Iterator references (`<iterator>.value.field`) are rewritten to `item.field`. Real kro form (matches `examples/aws/aws-accounts-factory/01-network-stack.yaml`). |
| `output "X" { value = ... }` | `spec.schema.status.X` (best-effort) |

Anything not statically resolvable becomes a `${...}` CEL expression — the generator walks the `ast` field on every export expression and emits the equivalent CEL.

## What the export schema gives you

The `{text, value?, ast?}` triple on every expression is exactly the shape a converter needs:

- **text** — the canonical source. Useful for fallback rendering and error messages.
- **value** — the cty-marshalled JSON when the curated stdlib resolves the expression statically. Lets the generator emit clean YAML literals for the common case (`subnet_ids = ["subnet-aaaa", "subnet-bbbb"]` becomes a real YAML list, not a quoted string).
- **ast** — the structural decomposition as a tagged JSON tree. Lets the generator translate expressions to the target language (here CEL) with one switch statement per node kind. Especially valuable if the generator isn't written in Go (no need to embed an HCL parser).

The `dependencies` adjacency map lets the generator topologically sort resources or detect cycles, though kro itself resolves order from the CEL refs in the `${resources.foo.status.X}` syntax — the dependency graph is mostly informational.

## Subtleties the POC handles

### Parameterisation vs static evaluation

A field like `name = var.cluster_name` exposes BOTH a `value` (resolved statically because the variable defaults to `"demo"`) AND an `ast` (a `scope_traversal` to `var.cluster_name`). Naively emitting the value would lose the parameterisation — instances of the RGD couldn't override `cluster_name` at apply time. The generator's `expr_to_emit` walks the AST checking for any `scope_traversal`/`relative_traversal`/`splat` node; if present, it emits the CEL form regardless of whether `value` is also there. Pure-literal expressions still get the clean structured value.

### ARN convention

ACK exposes every resource's AWS ARN at the standard path `status.ackResourceMetadata.arn`. The generator special-cases the `.arn` attribute when emitting cross-resource references, mapping `aws_iam_role.cluster.arn` to `${resources.cluster.status.ackResourceMetadata.arn}` rather than the literal `${resources.cluster.status.arn}`.

### `format()` template expansion

`format("%s-eks-role", var.cluster_name)` expands into a CEL string-concat expression: `(schema.spec.cluster_name + "-eks-role")`. The generator only handles `%s` and `%d` on literal-string templates — production converters would either expand the full printf grammar or call into a CEL helper function.

### Dynamic blocks

`dynamic "name" { for_each = ..., iterator = ..., content { ... } }` blocks are surfaced separately from static blocks in the export's `dynamic_blocks` field. Each instance carries:

- `for_each` as a full `{text, value?, ast?}` expression
- `iterator` (empty when the source omitted it; consumers default to the block label)
- `content` as a recursive `ExportBlock` whose attribute expressions reference the iterator variable via `<iterator>.value.X` / `<iterator>.key`

The generator's `emit_dynamic` translates one dynamic block into a CEL `.map()` expression over the for_each source, with the per-iteration content body inlined as a CEL object literal. Iterator references are rewritten via `ast_to_cel_with_iterator`: `ingress.value.from_port` → `item.from_port`, etc.

This matches the actual kro RGD pattern used in real-world examples (see [`examples/aws/aws-accounts-factory/01-network-stack.yaml`](https://github.com/kubernetes-sigs/kro/blob/main/examples/aws/aws-accounts-factory/01-network-stack.yaml) in the kro repo: `publicSubnetIds: ${publicSubnets.map(s, s.status.subnetID)}`). For the security-group fixture in this directory the result is:

```yaml
ingressRules: "${schema.spec.ingress_rules.map(item, {\"cidrBlocks\": item.cidrs, \"fromPort\": item.from_port, \"protocol\": \"tcp\", \"toPort\": item.to_port})}"
```

Note: kro's `.map()` is intended for list iteration. Map-typed `for_each` (`for_each = { a = "x", b = "y" }` exposing `.key` and `.value`) would need a different translation — kro doesn't have a direct equivalent of Terraform's map iteration, and a converter would either rewrite to a list-of-objects or generate one CEL expression per known key. The POC handles the list-of-objects case (the common one); map iteration is left as future work.

### `jsonencode` round-trip

A statically-resolvable `jsonencode({...})` (where the body has no `var.X` / `local.X` / resource refs) collapses to a literal string at the export layer — the cty stdlib evaluates the function and the generator emits the JSON directly into YAML. Example: the IAM `assume_role_policy` in the fixture becomes `assumeRolePolicyDocument: "{\"Statement\":...}"` with no CEL needed. When the body DOES reference variables/resources, the generator falls back to `${json.marshal({...})}` using kro's CEL `json` library — no information loss.

This relies on a subtle HCL detail: a bare identifier in an object-cons key position (`Version = ...`, `Statement = ...`) is a literal STRING per [hclsyntax.ObjectConsKeyExpr](https://pkg.go.dev/github.com/hashicorp/hcl/v2/hclsyntax#ObjectConsKeyExpr), not a variable reference. The export AST walker special-cases this — emitting it as a `scope_traversal` would mislead consumers into thinking `{ Name = "x" }` references a variable named `Name`.

## Known gaps surfaced by the POC

| Limitation | Workaround for now |
| --- | --- |
| Map-typed `for_each` on dynamic blocks (`for_each = { a = ..., b = ... }`, with `<iterator>.key` / `<iterator>.value` semantics) | The export captures the for_each + content correctly, but the POC's `emit_dynamic` only handles the list-of-objects case (CEL `.map()`). Map iteration would either need rewriting to a list-of-objects upstream or generating one CEL expression per known key. See the dedicated "Dynamic blocks" subsection above. |
| Many AWS resource types lack ACK CRDs | Per-resource manual mapping in `ACK_MAPPING`. The [aws-controllers-k8s/code-generator](https://github.com/aws-controllers-k8s/code-generator) repo ships these mappings — a production converter would generate the table from there rather than hand-curate. |
| Splat expressions (`aws_subnet.example[*].id`) emit as `.map(x, ...)` in CEL | CEL doesn't have splat semantics; the rewrite is approximate. May need a kro extension or manual review. |
| `count = X` with non-trivial `X` | Translates to a CEL expression but kro's loop semantics are different from Terraform's. The POC doesn't currently handle `count` at all in the resource emitter — production code would generate one resource per count value or fold into a kro for-each construct when available. |
| Locals (`local.foo`) | The POC emits a `<local: foo>` marker when it sees `local.X` references. A real converter would inline locals into their consumers since RGDs have no `local` concept. |

## Effort estimate to productionise

The export gives a converter the parser/analyser/evaluator/dependency-graph layers — usually the hardest 60% of building one of these tools. What's left is roughly:

- **AST → target-language emitter** (~300-500 LOC, one switch case per node kind). This POC's `expr_to_cel` is the template.
- **Type/attribute mapping table** for the target system. The POC has hand-curated entries for two resource types; a production converter would generate the table from upstream metadata (ACK code-generator specs, Pulumi schema, CDK type definitions, etc.).
- **YAML/IDL emitter** for the target shape (~100-300 LOC).

Adding new Terraform resource types to this generator is mechanical: extend `ACK_MAPPING` with the resource's `apiVersion`, `kind`, and per-attribute renames. Extending the AST walker for new expression kinds is local to `expr_to_cel`/`call_to_cel`.

## Why not just use `terraform show -json`?

Two reasons:

1. **`terraform show -json` requires a successful `terraform plan`** — provider credentials, real state backend, network access to the registry. Static converters operating on source files alone can't use it.
2. **`terraform show -json` emits the *resolved* configuration** — variables, locals, module composition all collapsed into the plan. That's the wrong abstraction level for a converter that wants to preserve parameterisation in the target system. `tflens export` keeps variables and locals as first-class entities so the converter can map them to the target's parameterisation primitives (kro `schema`, Pulumi `Config`, CDK `parameters`, …).

`tflens export` sits between `hcl.File` (no type inference, no cross-module resolution) and `terraform show -json` (fully evaluated, requires plan): schema-free, providerless, no plan required, but with the type and dependency information that raw HCL doesn't give you.
