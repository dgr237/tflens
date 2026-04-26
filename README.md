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
- `--format markdown` — emit GitHub-flavoured markdown on stdout (severity badges 🔴🟡🔵, collapsible `<details>` per module with the most-severe sections opened by default, code-fenced fix hints inline, file:line as inline backticks). Designed for sticky-commenting on PRs and GitHub Actions step summaries (`>> $GITHUB_STEP_SUMMARY`). Like `--format json`, the whole document is a single stream — warnings stay on stdout — so it can be piped directly into `gh pr comment`. Currently rich for `diff`/`whatif`/`statediff`/`validate`; other subcommands get a terse markdown rendering.
- `--offline` — disable registry and git fetches; only local paths and `.terraform/modules/modules.json` entries are resolved.

```
tflens --format json validate ./my-tf | jq '.cross_module_issues[]'
tflens --format markdown diff --ref main ./my-tf | gh pr comment $PR --body-file -
tflens --offline diff --ref main ./my-tf
```

## GitHub Action

A composite action wrapper lives at the repo root, so a workflow can invoke `tflens diff --format markdown --enrich-with-plan` and post the result as a sticky PR comment in one step:

```yaml
permissions:
  contents: read
  pull-requests: write   # required for the sticky comment

jobs:
  tflens:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0   # tflens needs history for --ref comparison

      # Optional: produce a plan to enrich the diff with attribute-level deltas
      - run: terraform init && terraform plan -out=tfplan && terraform show -json tfplan > plan.json

      - uses: dgr237/tflens@v0.12.0
        with:
          command: diff
          ref: origin/${{ github.base_ref }}
          plan: plan.json
```

The action builds tflens from the same ref the consumer pinned (so `dgr237/tflens@v0.12.0` always runs tflens v0.12.0) and posts the markdown output as a sticky PR comment — subsequent runs edit the same comment via a hidden marker (`<!-- tflens-action:tflens -->`) instead of stacking new ones. Every run also appends to `$GITHUB_STEP_SUMMARY` for visibility on the workflow run page.

Inputs (all optional):

| Input | Default | Notes |
|---|---|---|
| `command` | `diff` | `diff` / `whatif` / `statediff` / `validate` |
| `path` | `.` | Project path |
| `ref` | `auto` | Git ref for diff/whatif/statediff. `auto` resolves to @{upstream} → origin/HEAD → main → master |
| `format` | `markdown` | `markdown` / `json` / `text`. PR commenting and step summary skip silently for non-markdown |
| `plan` | _(empty)_ | Path to `terraform show -json` output. Forwarded as `--enrich-with-plan` (diff only) |
| `state` | _(empty)_ | Path to a Terraform state file. Forwarded as `--state` (statediff only) |
| `args` | _(empty)_ | Raw extra args appended to the command (e.g. `--offline`) |
| `comment-on-pr` | `true` | Post / edit a sticky PR comment |
| `comment-tag` | `tflens` | Marker used to identify the sticky comment. Use distinct tags when calling the action multiple times in one workflow |
| `pr-number` | _(empty)_ | PR number to comment on. Required for non-`pull_request` triggers |
| `step-summary` | `true` | Append output to `$GITHUB_STEP_SUMMARY` |
| `fail-on-breaking` | `true` | Exit non-zero when tflens reports findings (CI gate) |

Outputs: `output-file` (path to captured output), `exit-code` (numeric), `breaking` (`true` / `false`).

## Commands

New here? Run through [Getting started](docs/getting-started.md) first. Comparing tflens against TFLint / Checkov / Terrascan / `terraform validate` / `terraform plan`? See [tflens vs other Terraform tools](docs/comparison.md).

The four primary commands all have dedicated reference pages under [`docs/commands/`](docs/commands/):

| Command | View | Reference |
|---|---|---|
| `validate <path>` | Single-tree | [`docs/commands/validate.md`](docs/commands/validate.md) — undefined references, type errors, sensitive-value leaks, `for_each` / `count` misuse. |
| `diff [path]` | Author | [`docs/commands/diff.md`](docs/commands/diff.md) — what changed in the module's API between this branch and the base ref. Optional `--enrich-with-plan` for attribute-level deltas. |
| `whatif [path] [name]` | Consumer | [`docs/commands/whatif.md`](docs/commands/whatif.md) — would my parent still work after this upgrade? |
| `statediff [path]` | Operator | [`docs/commands/statediff.md`](docs/commands/statediff.md) — which state instances may be added, destroyed, or re-instanced? |

Plus one foundational concept and one experimental command:

| | |
|---|---|
| [`# tflens:track` markers](docs/commands/tracked-attributes.md) | Opt resource attributes into the `diff` output — the source-only alternative to `--enrich-with-plan`, designed for module-developer CI. |
| [`tflens export`](docs/commands/export.md) | (experimental) Emit the enriched module model as JSON. Building block for converters to other provisioning systems. Shape is versioned and explicitly experimental. |

### Utility commands

Less commonly used; documented inline since the one-line description is sufficient.

| Command | Purpose |
| --- | --- |
| `inventory <path>` | List all declared entities (variables, locals, resources, data sources, modules, outputs) with source locations |
| `deps <path> <id>` | Show what an entity depends on and what depends on it |
| `impact <path> <id>` | Show every entity transitively affected if `<id>` changes, in topological order |
| `unused <path>` | Report entities nothing else in the module references |
| `cycles <path>` | Detect and print dependency cycles; exits non-zero if any found |
| `graph <path>` | Emit the dependency graph in Graphviz DOT format |
| `fmt <file.tf>` | Print normalised HCL; `-w` rewrites in place, `--check` exits 1 when unformatted |
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

1. **TFE tokens YAML file**, opt-in via `$TFLENS_TFE_TOKENS_FILE`. Some Terraform Enterprise org-management tooling ships per-organisation tokens in this format:

   ```yaml
   tokens:
     - address: tfe.example.com
       token: your-tfe-token
     - address: https://other.tfe.example.com
       token: another-tfe-token
   ```

   `address` may be a bare host, a `host:port` pair, or a full URL — only the host (with port if non-default) is matched against the outgoing request. Loading is strictly opt-in: with `$TFLENS_TFE_TOKENS_FILE` unset, tflens never reads from any conventional path. Set the variable to enable the source.

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
- **Limited expression evaluation.** Conditionals, arithmetic, string interpolation, and a curated set of ~46 Terraform built-in functions (see [Static evaluation surface](#static-evaluation-surface)) ARE evaluated when every reference resolves to a known constant — this powers the effective-value-collapse for tracked attributes and statediff sensitive locals. Anything that reaches a computed attribute (`aws_vpc.main.id` is always `TypeUnknown`), a data source, or a non-curated function (`templatefile`, `jsondecode`, `regex`, `try`, …) falls back to text comparison.
- **Caller awareness only at module-call boundaries.** `whatif` and the local-source path of `diff` cross-validate a parent module's `module "x" { ... }` block against the candidate child's variables and outputs. Beyond that — e.g. whether some external repo that pinned to an old version still works after a registry-module change — is out of scope; we'd need to analyse those callers too.
- **Mid-expression comments are dropped.** Line and block comments at statement boundaries round-trip correctly; comments embedded inside a function call argument list split across lines, or inside a multi-line object/tuple literal, are lost by `fmt`.

### Static evaluation surface

Tracked-attribute diffs and statediff sensitive-local detection both ask the question "does the new expression evaluate to the same value as the old one?". When the answer is yes, the change is suppressed — so a refactor like `"us-east-1"` → `lower("US-EAST-1")` doesn't gate CI.

The curated function set is intentionally a subset of Terraform's built-ins, not a complete mirror. Adding more is cheap (one entry in `pkg/analysis/stdlib/stdlib.go` plus a fixture) — see [issue #16](https://github.com/dgr237/tflens/issues/16) for the rationale on what's in vs. out.

| Group | Functions |
| --- | --- |
| Type conversion | `toset`, `tolist`, `tomap`, `tostring`, `tonumber`, `tobool` |
| Collections | `length`, `concat`, `merge`, `keys`, `values`, `lookup`, `contains`, `element`, `flatten`, `distinct`, `sort`, `reverse`, `slice`, `chunklist`, `compact`, `coalesce`, `coalescelist`, `zipmap`, `range`, `index` |
| Sets | `setunion`, `setintersection`, `setsubtract`, `setsymmetricdifference`, `setproduct` |
| String | `upper`, `lower`, `title`, `join`, `split`, `format`, `formatlist`, `replace` (literal + `/regex/` dispatch), `trim`, `trimspace`, `trimprefix`, `trimsuffix`, `chomp`, `indent`, `substr` |
| Regex | `regex` (string / tuple / object return depending on capture groups), `regexall` |
| Encoders / decoders | `jsonencode`, `jsondecode`, `csvdecode`, `base64encode`, `base64decode` |
| Numeric | `abs`, `min`, `max`, `floor`, `ceil`, `pow`, `parseint` |

**Deliberately excluded** (and unlikely to be added): filesystem (`file`, `fileset`, `templatefile`) — needs filesystem context that isn't valid for static analysis; non-deterministic (`timestamp`, `uuid`, `bcrypt`) and time-based hashing (`base64sha256`, `base64sha512`, `filebase64sha256`, `md5`, `sha1`, etc.) — must always return unknown or need crypto state that isn't pure-functional; complex semantics (`can`, `try`) — needs full Terraform evaluator catch-and-retry; `regexreplace` — Terraform exposes regex replacement only through the `/pattern/` form of `replace`, which the curated `replace` already dispatches; date/time helpers (`formatdate`, `timeadd`) — input is usually `timestamp()` (excluded) and standalone uses are rare; rarely-used numerics (`signum`, `log`) — almost never seen in real modules.

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
| `cache` | Content-addressable disk cache for downloaded module sources |
| `resolver` | Pluggable `Resolver` chain (local path, `.terraform/modules/modules.json`, Terraform Registry, git) with credential support |
| `tfstate` | Terraform state v4 JSON parser; exposes resource identity + instance keys for cross-reference |

The CLI layer lives in top-level `cmd/` (cobra), with one file per subcommand. `main.go` just calls `cmd.Execute()`.

The analysis is a three-pass design: collect entities → collect dependency edges → run type checking. The dependency graph powers `deps`, `impact`, `unused`, `cycles`, and the topological-sort output in `impact`.
