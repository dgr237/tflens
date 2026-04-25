# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo orientation

`tflens` is a static Terraform analyser written in Go (1.25+). It parses `.tf` files via `hashicorp/hcl/v2` (no Terraform execution, no provider schemas) and exposes a cobra CLI under `cmd/`. The README is comprehensive — read it for what each subcommand does and the classification rules. CONTRIBUTING.md covers commit-message style, the release flow, and the table-driven testdata convention; follow it.

## Common commands

```
make check           # vet + fmt-check + test  (run before every commit)
make test            # tests only
make coverage        # writes coverage.html
go test ./pkg/diff/... -run TestAnalyzeProjects -v
go test -coverprofile=cov.out -coverpkg=./... ./...   # full cross-pkg coverage
go tool cover -func=cov.out | grep -E '^total|<file>'
```

`go test ./...` is fine without the Makefile. Single subtests use the `Test/sub` form (`-run 'TestAnalyzeProjectsCases/local_child_breaking'`). The cmd-package tests are slow (~25s) because they exercise integration paths with `git worktree`.

## Architecture invariants

**Three-pass analysis.** `pkg/analysis.AnalyseFiles` runs `collectEntities` → `collectDependencies` → `typeCheckBodies` → tracked-attribute scan, in that order. Cross-file references resolve correctly because every entity is registered before any edge is collected. New entity kinds need to be added to all three passes plus `pkg/diff`'s entity dispatch.

**Module resolution chain.** `pkg/resolver.NewChain(manifest, local, registry, git)` is composed in `cmd/resolver.go::buildResolver` and consumed by `pkg/loader.LoadProjectWith`. Order matters: manifest (`.terraform/modules/modules.json`) and local-path are always tried; registry/git are skipped under `--offline`. Credentials come from `LoadTfeTokens` (opt-in via `$TFLENS_TFE_TOKENS_FILE`) merged with `LoadTerraformrc`, with TFE winning ties.

**Source-type drives diff classification.** In `pkg/diff.AnalyzeProjects`, local-source children (`./…`, `../…`) get the consumer view via `ConsumptionChangesForLocal` (cross-validate the parent's `module {}` block against the new child); registry/git children get the full API diff via `Diff(oldChild, newChild)`. `whatif` is consumer-view always, regardless of source. If you're touching the child-comparison path, decide which branch your change applies to.

**Tracked-attribute cross-module resolution.** `DiffTrackedCtx` takes parent context: a marker inside a child module is resolved through the parent's call argument and any locals/vars it transitively references. Effective-value awareness collapses text-different expressions whose evaluated values agree (e.g. `"1.34"` vs `var.upgrade ? "1.35" : "1.34"` with `var.upgrade=false`). Evaluation goes through `Module.EvalContext()` which wires only the cty stdlib — Terraform-specific functions (`length`, `contains`, `merge`, …) are NOT wired in and fall back to text comparison.

**`Module` getters are nil-safe.** `Backend`, `RequiredVersion`, `RequiredProviders`, `Moved`, `RemovedDeclared`, `Validate`, `ModuleSource`, `ModuleVersion`, `ModuleOutputReferences`, `Entities`, `Filter`, `HasEntity`, `EntityByID`, `TrackedAttributes`, `EvalContext`, `GatherRefsFromExpr` all handle a nil receiver. This is load-bearing for `Diff(nil, nil)` (called from `AnalyzeProjects` when one side has no root). When adding a new exported `Module` method called from `pkg/diff`, follow the same pattern.

**`cmd/` run* methods are slim by design.** Pattern is load → process → print, ≤~10 body lines. Heavy logic lives in `pkg/`. There is no cobra test harness — coverage of cmd/ comes from extracting logic into `pkg/` and testing the helpers there. Resist adding business logic to `cmd/`.

**Worktree-based git ref comparison.** `diff`, `whatif`, `statediff` all use `pkg/loader.PrepareWorktree` which calls `git worktree add` into a temp dir, with cleanup deferred. The "old" side is loaded from the worktree, "new" from cwd. `pkg/git` is the thin git plumbing wrapper; `pkg/loader/worktree.go` adds the workspace-prefix logic for sub-directory invocations.

## Testing conventions

Per CONTRIBUTING: table-driven tests with per-case fixtures under `testdata/<group>/<case>/main.tf`, loaded via `loadAnalysisFixture(t, group, name)`. The pattern in `pkg/analysis`:

```go
type fooCase struct { Name string; Custom func(t *testing.T, m *analysis.Module) }
func TestFooCases(t *testing.T) {
    for _, tc := range fooCases {
        t.Run(tc.Name, func(t *testing.T) {
            m := analyseFixtureNamed(t, "main.tf", loadAnalysisFixture(t, "<group>", tc.Name))
            tc.Custom(t, m)
        })
    }
}
```

`pkg/diff` cross-module fixtures use real project layout: `<case>/old/main.tf` + `<case>/old/modules/<call>/main.tf`, mirrored under `new/`.

When writing inline HCL in a test, **use newlines between attributes** — single-line blocks like `variable "x" { type = string default = "y" }` parse as "Invalid single-argument block definition" in HCL2.

## Coverage gotchas

- `go test ./pkg/X/...` without `-coverpkg` reports only that package's *self-coverage*. Cross-package callers don't count. Use `-coverpkg=./...` when judging a function's true coverage.
- A method covered indirectly via another package may show 0% in self-coverage even though it's heavily exercised. Total project coverage is the union (the "total:" line from `go tool cover -func`).
- `cmd/` coverage is structurally low because run* methods aren't directly invoked from tests — that's by design (extract to `pkg/` instead of adding a cobra harness).

## Platform notes

- Bash on Windows: use Unix paths (`/c/wip/...`), not `C:\wip\...`. Forward slashes everywhere.
- Tests must pass on Linux, macOS, and Windows. Windows bites: short-name paths (`RUNNER~1`), CRLF line endings (use `strings.Contains` for git output, not equality), `git worktree add` tolerating empty existing dirs (use `os.Remove` then add).
- macOS: `/private/var` symlinks. `git rev-parse --show-prefix` is preferred over `filepath.Rel` for "where am I in the repo" — both Windows and macOS surface canonicalisation mismatches there.

## Release flow

Don't run releases from a Claude session unless explicitly asked. The flow is in CONTRIBUTING.md §Releases — preferred path is the `release:patch|minor|major` PR label, which triggers `.github/workflows/auto-release.yml` on merge. Manual fallback is `make release-push VERSION=X.Y.Z` from a maintainer's checkout on `main`.

User-visible changes need a `## [Unreleased]` entry in CHANGELOG.md; pure refactors and tests don't (the `changelog-check` workflow auto-skips PRs whose only changes are under `*_test.go`, `*/testdata/*`, `.github/*`, `scripts/*`, or top-level `.md`).
