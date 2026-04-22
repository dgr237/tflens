# tflens

A standalone Terraform/HCL parser and analysis tool, written in Go. The only runtime dependency is [spf13/cobra](https://github.com/spf13/cobra) for the CLI layer; all parsing, analysis, diff, constraint, and module-resolution logic is dependency-free.

Parses `.tf` files into an AST, builds a dependency graph, validates references and types, and diffs two module versions to surface breaking changes. Does not execute Terraform and does not need provider schemas.

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
tflens --format json validate ./my-workspace | jq '.cross_module_issues[]'
tflens --offline diff --branch main ./my-workspace
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
| `diff <old> <new>` | Compare two module versions and classify changes as Breaking, NonBreaking, or Informational |
| `diff --branch <base> [ws]` | Compare every module call in a workspace against its counterpart at a git ref; reports per-call diffs and added / removed calls |
| `whatif <workspace> <module> <new-dir>` | Simulate upgrading a specific module call in a workspace to a candidate new version; report what would break |
| `whatif --branch <base> [ws] [name]` | Simulate every upgrade on the working tree against callers at `<base>`; with no `<name>`, every changed call is simulated |
| `cache info` | Show the cache location, entry count, and total size |
| `cache clear` | Delete every cached module |
| `lsp` | Run as a Language Server Protocol server over stdio (for editor integration) |

`<path>` is either a single `.tf` file or a directory (in which case all `.tf` files in it are merged into a single module view, matching Terraform's own behaviour).

## Parsing coverage

The lexer and parser handle the HCL subset used by Terraform:

- Blocks with labels (`resource "aws_vpc" "main" { ... }`)
- Attributes and nested blocks, including `dynamic` blocks
- All primitive literals (string, number, bool, null), heredocs
- Template strings with `${...}` interpolation
- Expressions: unary/binary operators with correct precedence, ternary `?:`, splat (`.*`, `[*]`), indexing, dot traversal
- Collections: tuples, objects with `=` or `:` separators
- `for` expressions for both lists and maps, with optional `if` clause
- Function calls (with `...` argument spread)
- Error recovery: a bad attribute or block does not prevent the rest of the file from parsing

Parse errors are reported with position information (`file:line:column`). Line (`#`, `//`) and block (`/* */`) comments are preserved through the parse → print round-trip: comments before a statement become leading comments, comments on the same line after a statement become trailing comments. Mid-expression comments (e.g. inside a function call argument list split across lines) are dropped.

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

Classifies every detected change as one of three kinds, then exits non-zero when any Breaking changes exist (suitable for CI gating):

- **Breaking** — existing callers or state will be affected
- **NonBreaking** — safe to upgrade through
- **Informational** — operational or cosmetic, but worth surfacing

### What it catches

**Variables:**
| Change | Kind |
| --- | --- |
| Variable removed | Breaking |
| New required variable (no default) | Breaking |
| New optional variable (has default) | NonBreaking |
| `default` removed (optional → required) | Breaking |
| `default` added (required → optional) | NonBreaking |
| Type widened to `any` | NonBreaking |
| Type changed (otherwise) | Breaking |
| Object field added (required) | Breaking |
| Object field added (optional) | NonBreaking |
| Object field removed | Breaking |
| Object field optional → required | Breaking |
| Object field required → optional | NonBreaking |
| `nullable = false` added | Breaking |
| `nullable = false` removed | NonBreaking |
| `sensitive = true` added | Breaking |
| `sensitive = true` removed | Informational |
| New `validation` / `precondition` / `postcondition` block | Informational |

**Outputs:**
| Change | Kind |
| --- | --- |
| Output removed | Breaking |
| Output added | NonBreaking |
| `sensitive` toggled | Informational |
| `value` expression changed (normalised) | Informational |
| Referenced `local` expression changed (indirect) | Informational |
| New `precondition` / `postcondition` | Informational |
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
| `count` / `for_each` *expression* changed (mode unchanged) | Informational |
| `provider = aws.east` → `aws.west` | Breaking |
| Lifecycle: `prevent_destroy` added/removed | Informational |
| Lifecycle: `create_before_destroy` toggled | Informational |
| Lifecycle: `ignore_changes` / `replace_triggered_by` changed | Informational |
| Lifecycle: new `precondition` / `postcondition` | Informational |
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

**Semver-aware version constraint comparison:**

Version constraints (`>= 1.0`, `~> 4.0`, `!= 1.2.3`, compound forms like `">= 1.0, < 2.0"`) are parsed into interval sets and compared:

- **Equal** — same satisfying set
- **Broadened** (old ⊂ new) — NonBreaking
- **Narrowed** (new ⊂ old) — Breaking ("tightened")
- **Overlap** (neither subset, some overlap) — Breaking ("partially narrowed")
- **Disjoint** — Breaking ("incompatible")
- **Unparseable** — falls back to Informational with a generic detail

### What it does NOT catch

- **Resource attribute body changes.** A resource gaining or changing an attribute (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) is not diffed. The noise-to-signal ratio of a full body diff is too low; if the resource is removed, renamed, or has its meta-arguments changed, we flag that — but individual attribute edits are treated as internal.
- **Resource provider schema changes.** We cannot tell that an AWS provider bump from v4 to v5 silently changed `aws_vpc.main.cidr_block` from a string to a list, because we do not embed provider schemas.
- **Dynamic block content.** `dynamic "ingress" { for_each = ... }` bodies generate blocks at plan time. Without evaluating the `for_each`, the generated block set is opaque.
- **Condition strictness.** If `validation { condition = length(var.x) > 0 }` becomes `condition = length(var.x) > 5`, we record that a validation block exists in both versions but cannot tell that the new condition rejects strictly more inputs.
- **Default value *content* changes.** Only the presence or absence of a default is diffed. Changing `default = "dev"` to `default = "prod"` produces no change — most real modules change defaults deliberately, and flagging every edit produces too much noise.
- **Description / documentation changes.** Informational-only, currently skipped.
- **Ambiguous renames.** The rename heuristic pairs exactly one removed entity with one added entity of the same kind and type. When there are multiple candidates (two removed, two added of the same type), no pairing is attempted — each is reported individually.
- **Type coercion subtleties.** `list(string)` → `set(string)` is flagged as a type change. Terraform auto-converts in some expression contexts but not others (index access `[0]` fails on sets); distinguishing safe from unsafe coercions requires knowing how each caller uses the variable, which is cross-module.
- **Type narrowing of custom objects without `optional()`.** Adding a field to an object type is correctly flagged as breaking, but this tool cannot reason about *what* the provider would accept for that field's value.
- **`check { assert { ... } }` blocks** (Terraform 1.5+) — not currently parsed.
- **`import { ... }` blocks** (Terraform 1.5+) — not currently parsed.
- **Backend configuration diffs** (`terraform { backend "s3" { ... } }`) — not currently parsed. More commonly in root modules than in reusable modules.
- **Provisioner blocks** (`provisioner "local-exec"`, `connection`) — not currently parsed; their presence or absence affects teardown and creation but is not flagged.
- **Nested moved-block expressions with indices.** `moved { from = aws_vpc.main[0], to = aws_vpc.main["a"] }` is not recognised; only bare resource references in `from` / `to` are parsed.
- **Cross-module diffs.** `tflens diff` compares two versions of the *same* module. It does not recursively diff parent + children together. A parent-module `source` bump is reported as Informational but not followed into the child.

## What-if upgrade analysis (`whatif`)

`whatif` answers: *if I bumped this module to a new version, would my current usage still work?* It has two modes.

### Explicit mode

```
tflens whatif <workspace> <module-call-name> <new-version-path>
```

Point at a workspace, the module call you want to simulate, and a directory containing the candidate new version (a local checkout at a tag, an extracted tarball, or a separate `.terraform/modules/<name>` tree).

1. Loads the workspace and locates the current version of `module.<name>` via the normal resolution rules.
2. Loads the candidate new version as a standalone module from `<new-version-path>`.
3. **Direct impact:** runs cross-validation of the parent's `module "<name>" { ... }` block against the *candidate* — reports missing required inputs, unknown arguments, and type mismatches the upgrade would introduce.
4. **Module API changes:** runs a full `diff` between the currently-installed child and the candidate for context (Breaking / NonBreaking / Informational).

### Branch mode

```
tflens whatif --branch <base> [workspace] [call-name]
```

The candidate new version is whatever the working tree resolves to. The "current" caller is the workspace checked out at git ref `<base>`. Useful for CI-gating an upgrade PR: on a feature branch that bumps `version = "..."` or refactors a local child, run `tflens whatif --branch main` to see whether callers on main would break.

With no `call-name`, every module call that differs between `<base>` and the working tree is simulated and aggregated; pass a name to scope to one call.

Both modes exit non-zero when the direct-impact list is non-empty — suitable for CI gating.

`tflens diff --branch <base> [workspace]` is the complementary command: same workspace-vs-base comparison, but reports the full API diff per module call rather than cross-validation. Use `diff --branch` to review a module-bump PR; use `whatif --branch` to gate it.

## Module resolution

Commands that traverse a workspace (`validate`, `whatif`, `diff --branch`) turn each `module "x" { source = "..." }` call into a directory on disk via a chain of resolvers, tried in order:

1. **Local path** — `source = "./x"` and `source = "../y"` resolve relative to the caller's directory. Always tried.
2. **`.terraform/modules/modules.json`** — if the manifest produced by `terraform init` is present, every module call is resolved through it by dotted key path (`vpc`, `vpc.sg`, etc.). Always tried.
3. **Terraform Registry** — sources of the form `ns/name/provider` or `host/ns/name/provider` (plus optional `//subdir`). Service discovery (`/.well-known/terraform.json`) → version list → `version` constraint (`~>`, `>=`, `<`, `=`, `!=`) resolved to a concrete version → tarball or git download → extracted into the cache. Skipped in `--offline` mode.
4. **Git** — `source = "git::<url>"` (HTTPS, SSH, or file:// for tests) plus the bare VCS shorthand `github.com/foo/bar`, `bitbucket.org/foo/bar`, `gitlab.com/foo/bar`, `codeberg.org/foo/bar`. Honours `?ref=` and `//subdir`. Skipped in `--offline` mode.

### Cache

Downloaded modules are stored under the OS user cache directory (e.g. `~/.cache/tflens/modules` on Linux, `%LocalAppData%\tflens\modules` on Windows). The cache is content-addressable and immutable: a given (host, path, concrete-version) tuple is only ever downloaded once. Delete the directory to force re-fetches.

### Private registries

Credentials for private registries are read from the Terraform CLI config file (`$TF_CLI_CONFIG_FILE`, or `%APPDATA%\terraform.rc` on Windows, or `~/.terraformrc` elsewhere). The format is identical to Terraform's:

```
credentials "app.terraform.io" {
  token = "your-api-token"
}

credentials "registry.example.com" {
  token = "..."
}
```

Bearer tokens are sent **only** to requests whose `URL.Host` exactly matches a configured entry. This means a registry that redirects its tarball download URL at a third-party CDN (typical for GitHub-backed public modules) never receives the token.

### Version constraints

`~>`, `>=`, `<=`, `>`, `<`, `=`, `!=` are supported, comma-separated clauses are intersected. Version comparison follows SemVer 2.0.0, including prerelease precedence (§11). Prerelease matching is currently mathematical: `1.5.0-beta` satisfies `>= 1.0.0`.

## Fundamental limitations

These are not bugs but deliberate boundaries:

- **No Terraform execution.** This is a static analyser. Anything that requires planning, applying, or querying a provider is out of scope.
- **No provider schemas.** We do not embed AWS/GCP/Azure/etc. provider schemas. Resource attribute types, required-vs-optional attributes, and deprecations of resource types are invisible to us. Running `terraform validate` in addition to this tool catches those.
- **No expression evaluation.** Functions, conditionals, and references to computed attributes cannot be resolved statically. The inferred type of `aws_vpc.main.id` is `TypeUnknown`.
- **No caller awareness.** We analyse a module in isolation. Whether an existing caller actually uses a removed output, or pinned to the now-incompatible version, cannot be determined without analysing callers too.
- **Mid-expression comments are dropped.** Line and block comments at statement boundaries round-trip correctly; comments embedded inside a function call argument list split across lines, or inside a multi-line object/tuple literal, are lost by `fmt`.

## Editor integration (`lsp`)

`tflens lsp` speaks JSON-RPC 2.0 over stdio and exposes the following LSP capabilities:

- **Diagnostics** — parse errors, undefined references, type errors, `for_each`/`count` misuse, sensitive-leak warnings, all surfaced as inline markers as you type
- **Hover** — type, defaults, sensitive/nullable flags, and declared position for the entity under the cursor
- **Go-to-definition** — jump from a reference (`var.x`, `local.y`, `module.z`, `data.a.b`, resource refs) to its declaration
- **Document symbols** — file outline listing every variable, local, resource, data source, module, and output
- **Completion** — context-scoped suggestions triggered by `.`:
  - `var.` → only variables
  - `local.` → only locals
  - `module.` → only module calls
  - `data.` → all data sources (as `type.name`); `data.TYPE.` narrows to instances of that type
  - `<resource_type>.` → instances of that resource type
- **Formatting** — format-on-save using the same idempotent printer as `tflens fmt`

Logs go to stderr; stdout is reserved for the protocol.

### Hookup

**Neovim (nvim-lspconfig):**

```lua
vim.lsp.config.tflens = {
  cmd = { 'tflens', 'lsp' },
  filetypes = { 'terraform', 'tf' },
  root_markers = { '.terraform', '.git' },
}
vim.lsp.enable('tflens')
```

**Helix (`.helix/languages.toml`):**

```toml
[language-server.tflens]
command = "tflens"
args = ["lsp"]

[[language]]
name = "hcl"
language-servers = ["tflens"]
```

**Zed, Emacs (lsp-mode/eglot), Sublime (LSP), JetBrains IDEs (LSP plugin):** point the LSP client at `tflens lsp` with `terraform` as the language/filetype.

**VS Code:** requires a small extension wrapper (not yet shipped). Any volunteer-written extension that launches `tflens lsp` as the server binary will work.

### Out of scope for v1

- Rename, code actions, inlay hints, semantic tokens — doable, just not shipped yet.
- Cross-module diagnostics as you type — only single-file validation runs on `didChange`. Project-level cross-module checks happen on `didSave`. A future version could re-run `LoadProject` with in-memory overrides on every keystroke (or debounce it heavily).
- Incremental parsing — the whole file is re-parsed on every change. Fast enough for any reasonable Terraform module; large generated files might hiccup.

## License

Apache License, Version 2.0. See [LICENSE](./LICENSE).

Copyright 2026 David Roberts.

## Architecture

Code is organised under `pkg/`:

| Package | Responsibility |
| --- | --- |
| `token` | Token types and position records |
| `lexer` | Mode-stack lexer producing tokens from raw bytes |
| `ast` | AST node types, visitor, inspect |
| `parser` | Recursive-descent / Pratt parser with error recovery |
| `printer` | Idempotent AST-to-HCL printer (including `PrintExpr` for single expressions) |
| `analysis` | Entity collection, dependency graph, type system, validation, graph algorithms (cycles, topo-sort, impact, unreferenced) |
| `loader` | Multi-file / directory / recursive submodule loading, cross-module input validation |
| `diff` | Two-module comparison with semver-aware constraint classification |
| `constraint` | SemVer parsing and Terraform-style version constraint evaluation (`~>`, `>=`, ...) |
| `cache` | Content-addressable disk cache for downloaded module sources |
| `resolver` | Pluggable `Resolver` chain (local path, `.terraform/modules/modules.json`, Terraform Registry, git) with credential support |

The CLI layer lives in top-level `cmd/` (cobra), with one file per subcommand. `main.go` just calls `cmd.Execute()`.

The analysis is a three-pass design: collect entities → collect dependency edges → run type checking. The dependency graph powers `deps`, `impact`, `unused`, `cycles`, and the topological-sort output in `impact`.
