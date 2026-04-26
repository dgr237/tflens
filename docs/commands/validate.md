# `tflens validate`

Static validation of a single Terraform project — no diff, no base ref, no plan. Surfaces the kind of errors `terraform validate` would catch at plan time, plus a few that need cross-module reasoning.

## When to use it

- **PR review** on the working tree — fast feedback before `terraform plan`.
- **Pre-commit hook** — runs in milliseconds on the changed files.
- **Author-side gate** when adding or changing a module — catches typos and structural mistakes before they reach a consumer.

## Usage

```
tflens validate [path]
```

`path` defaults to cwd. Accepts a single `.tf` file or a directory (in which case all `.tf` files in it are merged into one module view, matching Terraform's own behaviour).

Exits non-zero if any validation error is reported.

Global flags that apply: `--format json` / `--format markdown`, `--offline`.

## Examples

### Undefined reference

```hcl
# main.tf
variable "region" { type = string }

output "where" {
  value = var.regio   # ← typo
}
```

```bash
tflens validate .
```

```
main.tf:4:11: undefined variable: var.regio (did you mean var.region?)
```

### Type mismatch on a default

```hcl
variable "max_size" {
  type    = number
  default = "five"   # ← string default for a number
}
```

```
main.tf:3:13: variable.max_size: default type mismatches declared type (string default for number)
```

### `for_each` of a list (silently broken in Terraform too)

```hcl
variable "regions" { type = list(string) }

resource "aws_subnet" "public" {
  for_each = var.regions   # ← needs a set or map, not a list
  cidr_block = "10.0.0.0/24"
}
```

```
main.tf:4:14: aws_subnet.public: for_each requires a set or map; got list(string)
  hint: convert with toset(var.regions) or use count
```

### Sensitive-value leak in an output

```hcl
variable "db_password" {
  type      = string
  sensitive = true
}

output "creds" {
  value = var.db_password   # ← output not marked sensitive
}
```

```
main.tf:7:11: output.creds: references a sensitive variable but is not itself marked sensitive
  hint: add `sensitive = true` to the output, or stop returning the sensitive value
```

### Cross-module input check (parent → child)

When `path` is a directory containing module calls, every `module "x" { source = "..." }` is recursively checked against the child's variables:

```hcl
# parent/main.tf
module "vpc" {
  source = "./modules/vpc"
  cidir  = "10.0.0.0/16"   # ← typo on the argument
}
```

```hcl
# parent/modules/vpc/variables.tf
variable "cidr" { type = string }
```

```
parent/main.tf:3:3: module.vpc: unknown argument "cidir" — child module modules/vpc has no variable "cidir" (did you mean "cidr"?)
  hint: remove the argument from the module block, or restore the matching variable in the child
```

## What it catches

**Undefined references:**

- `var.X` referencing an undeclared variable
- `local.X` referencing an undeclared local
- `module.X` referencing an undeclared module call
- `data.X.Y` referencing an undeclared data source

**Type errors on variable defaults:**

- `variable "x" { type = number, default = "three" }` — default type mismatches declared type
- Object field types (including `optional(T)` wrapping) compared structurally
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

See [main README — Module resolution](../../README.md#module-resolution) for how `source = "..."` is turned into a directory on disk. A broken `modules.json` is reported as a warning but does not abort the rest of the validation.

## What it does NOT catch

- **Resource-style references** (`aws_vpc.main.id`) to undeclared resources. `for` expressions introduce unbound iterator variables (`item`, `v`, `k`, ...) and treating every unknown two-part reference as an undefined resource produces too many false positives without scope-aware analysis.
- **Function argument types.** Only return types are modelled. Passing the wrong type *into* a function is not flagged (Terraform will catch it at plan time with higher fidelity than we could).
- **Provider attribute types.** The shape of `aws_vpc.main.cidr_block` depends on the AWS provider schema, which this tool does not embed.
- **Condition semantics not *evaluated*.** `validation { condition = ... }`, `precondition`, and `postcondition` blocks have their condition text captured and diffed (adding/removing/replacing a condition surfaces as Informational), but the boolean isn't evaluated — so we can't tell you whether `length(var.x) > 5` would pass for a given input value, nor whether one constraint is strictly broader/narrower than another.
- **`count` with string literals.** Terraform silently coerces `"3"` to `3`, so we allow it.
- **Cross-module validation in `--offline` mode for unresolved remote sources.** When `--offline` is set, parent → child checks require the child's directory to be resolvable — either via a local path (`./x`, `../y`) or via the post-`terraform init` manifest at `.terraform/modules/modules.json`. Registry and git sources cannot be loaded in that mode and are silently skipped. Either run `terraform init` first or drop `--offline`.
- **Cross-module validation where argument types are opaque.** A parent passing `aws_vpc.main.cidr_block` to a typed child variable produces no type-mismatch error because the resource attribute's type cannot be resolved without provider schemas.
- **Runtime values.** Defaults that call `timestamp()`, `uuid()`, or similar are not evaluated.
- **Build metadata in semver.** Stripped during parsing per SemVer 2.0.0 §10. Prerelease identifiers are preserved and ordered per §11 (via `hashicorp/go-version`, the same library Terraform uses).

## Output formats

Default is plain text. `--format json` emits a structured envelope:

```json
{
  "validation_errors": [
    { "entity": "var.regio", "msg": "undefined reference", "pos": { "file": "main.tf", "line": 4, "column": 11 } }
  ],
  "type_errors": [...],
  "cross_module_issues": [...]
}
```

`--format markdown` renders for PR-comment use (badges, code-fenced positions, fix hints).

## Related

- **[`docs/commands/diff.md`](diff.md)** — once validation passes, diff catches API-surface changes vs. a base ref.
- **[main README — Module resolution](../../README.md#module-resolution)** — how cross-module checks resolve `source = "..."`.
