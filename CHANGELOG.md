# Changelog

All notable changes to tflens are documented here. The format is loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and tflens adheres to [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed (internal)

- **`analysis.Module` getters are now nil-safe.** `Backend`, `RequiredVersion`, `RequiredProviders`, `Moved`, `RemovedDeclared`, `Validate`, `ModuleSource`, `ModuleVersion`, `ModuleOutputReferences`, `Entities`, `Filter`, `HasEntity`, `EntityByID`, `TrackedAttributes`, `EvalContext`, and `GatherRefsFromExpr` now return their zero value (or an empty map / nil slice) when called on a nil receiver instead of panicking. Lets `pkg/diff.Diff(nil, nil)` and `AnalyzeProjects(nil, nil)` work as no-ops, which the cmd layer relies on when one side of a comparison has no root module.
- **Resolver-chain composition + project-loading helpers extracted from `cmd/` to `pkg/loader`** as exported `DefaultResolverChain(absRoot, offline)`, `LoadProjectDefaults(rootDir, offline)`, `LoadProjectsForDiff(path, baseRef, offline)`, and `LoadForValidate(path, offline)`. The cmd layer's `loadProject`, `loadOldAndNew`, `loadOldProjectForRef`, `buildResolver`, and `loadForValidate` are now thin cobra-side wrappers (or deleted entirely, in the case of `loadOldProjectForRef` and `buildResolver`) that read `--offline` from the command flags and delegate to the loader. Same behaviour, but the chain composition is now unit-testable directly — 9 new tests cover offline mode, the manifest-warning seed, relative-path absolutisation, missing-ref errors, and the file-vs-dir dispatch in `LoadForValidate`.
- **New `pkg/config` package centralises cobra flag parsing as a `Settings` struct.** `config.FromCommand(cmd)` reads every recognised tflens flag once and returns a plain (cobra-free) struct that subcommands thread through their `run*` helpers. `RefAutoKeyword` moves there too. Deletes the cmd-side `outputJSON`, `loadProject`, `loadOldAndNew`, `loadForValidate`, and `resolveAutoRef` cobra wrappers — every `run*` helper now takes `config.Settings` (or plain values) instead of `*cobra.Command`, and the flag-name strings (`offline`, `format`, `ref`, `state`, `write`, `check`) live in exactly one place. The Lookup-then-Get pattern in `FromCommand` lets a subcommand omit any flag without panicking. 4 new tests cover happy-path flag reading, unregistered-flag fall-through, the `--format=text` JSON guard, and the empty-args defaults.

### Added

- **`CLAUDE.md`** at the repo root documenting architecture invariants for AI-assisted development sessions: three-pass analysis ordering, resolver chain composition, source-type-driven diff classification, the cty-stdlib-only `EvalContext`, the nil-safe getter convention, the slim run* convention in `cmd/`, and the testdata + table-driven test pattern.

### Tests

- New table-driven coverage for `pkg/diff/AnalyzeProjects`, `AnalyzeWhatif`, `analysis.Module` `LookupAttrText` / `EvalContext` / `GatherRefsFromExpr`, the terraform / moved / removed / module-call getters, and `Expr.Range` / `Expr.Pos`. Fixtures live under `pkg/analysis/testdata/{tracked,module_getters}/` and `pkg/loader/testdata/loader/`. `pkg/loader/loader_test.go`'s 13 individual `TestLoadXxx` functions collapsed into `TestLoadDirCases` + `TestLoadProjectCases` table tests with discoverable subtests.

## [0.2.1] — 2026-04-24

### Changed (internal)

- **Module-call pairing logic moved from `cmd/` to `pkg/loader`** as exported `PairModuleCalls`, `ModuleCallPair`, `ModuleCallStatus`. Same behaviour; now unit-tested directly rather than only via subprocess integration tests.
- **Diff helpers moved from `cmd/diff.go` to `pkg/diff`** as exported `PairResult`, `ConsumptionChangesForLocal`, `HintForCrossValidateMsg`, `ExitCodeFor`. Same behaviour; now unit-tested directly. `cmd/diff` is now a thin orchestration + rendering layer over `pkg/diff` + `pkg/loader`.
- **Text-rendering helpers extracted to a new `pkg/render` package** (`WriteChange`, `BucketByKind`, `WriteChangesByKind`). `cmd/diff.go` and `cmd/whatif.go` previously each had their own bucket-by-Kind + emit-section pattern; now they share one io.Writer-based implementation with 100% coverage from unit tests using `bytes.Buffer`.
- **Whatif result type + builder moved from `cmd/whatif.go` to `pkg/diff`** as exported `WhatifResult` + `BuildWhatifResult`. Same behaviour; covered by 5 new unit tests including the no-old-parent / no-new-child / clean-upgrade edge cases.
- **Statediff analysis logic extracted to a new `pkg/statediff` package**. The whole resource-identity diff + sensitive-change detection (~553 LOC) moved out of `cmd/statediff.go`, leaving the cmd file as cobra wiring + text rendering only (730 LOC → 161 LOC). Public surface: `Analyze`, `Result` + `FlaggedCount`, `ResourceRef`, `RenamePair`, `SensitiveChange`, `AffectedResource`. Covered by 12 unit tests including added/removed/renamed flows, sensitive-local-reaches-count, for_each set narrowing, variable default reaching count, and rename-via-moved-block suppression.
- **Statediff text rendering moved to `pkg/render`** as `WriteStatediff`. `cmd/statediff.go` shrinks further (161 → 99 LOC) and is now purely cobra wiring + a four-line invocation chain (load → analyze → render or JSON → exit). 9 new tests in `pkg/render/statediff_test.go` cover the empty-result baseline message, added/removed/renamed sections, sensitive changes with and without state instances, the `(absent)` rendering of empty values, state orphans, and inter-section blank-line spacing.
- **JSON-shape adapters + struct types extracted from `cmd/output.go` to `pkg/render/json.go`**. Five wire-format types (`JSONPosition`, `JSONEntity`, `JSONValidationError`, `JSONTypeError`, `JSONChange`) plus their constructor functions (`JSONPos`, `JSONEnt`, `JSONValErr`, `JSONTypeErr`, `JSONChg`) now live in one place and are imported by the six cmd files that build JSON output (`diff`, `whatif`, `validate`, `inventory`, `unused`, plus the `output.go` skeleton). Wire format unchanged — same JSON tag names + values. 10 new unit tests cover field propagation, the omitempty contract on positions, and the `Kind` label strings (`breaking` / `non-breaking` / `info`). `cmd/output.go` shrinks 130 → 28 LOC.
- **Diff + whatif JSON envelope structs and builders moved to `pkg/render/diff_json.go`**: `JSONDiffOutput`, `JSONDiffModule`, `JSONWhatifOutput`, `JSONWhatifCall` plus the shared `JSONSummary` (Breaking/NonBreaking/Informational counts, with `JSONWhatifSummary` embedding it and adding `DirectImpact`). Builder functions `BuildJSONDiff` and `BuildJSONWhatif` now live with the structs they build, callable from cmd via a single line. Wire format byte-identical to the previous `refJSON` / `whatifBranchJSON` shapes — same field names, same JSON tags. 9 new unit tests cover empty-input / per-module-summary / overall-summary aggregation / uninteresting-pair filtering / wire-format stability / JSONWhatifSummary embedding.
- **Diff + whatif text rendering moved to `pkg/render/diff.go`**: `WriteDiffResults`, `WriteRootChanges`, `WritePairResult`, `WriteWhatifResults`, `WriteWhatifCall`. cmd/diff.go's `printRefResults` / `printRootChanges` / `printOneRefResult` and cmd/whatif.go's `printWhatifBranchResults` / `printOneWhatifCall` are gone — both subcommands now invoke the renderer in a single line. Identical output shape (added/removed/changed headings, source/version arrows, blank-line separators, indent levels). 14 new unit tests cover the empty-input baselines, the source/version-changed heading variants, the "(content changed)" / "(no API changes)" markers, the rendering of removed calls, and inter-section blank-line spacing.
- **`mustLoadModule` body moved to `pkg/loader`** as `LoadFile(path)` (single .tf file) and `LoadAny(path)` (auto-dispatches on file vs directory). cmd/common.go's `mustLoadModule` is now a thin wrapper that calls `loader.LoadAny` and exits on error. 7 new tests in pkg/loader cover happy/missing/parse-error paths for `LoadFile` and the file/dir/missing-path dispatch for `LoadAny`.
- **`mustEntityExists` removed from `cmd/common.go`** in favour of `analysis.Module.HasEntity(id) bool` (and `EntityByID(id) (Entity, bool)` for callers that want the value too). Both methods are constant-time — they use the existing `byID` index. 4 new tests cover existence lookup, the value-and-ok variant, and nil-receiver safety.

No user-facing API changes — all extractions are internal package surfaces.

## [0.2.0] — 2026-04-24

### Changed

- **`pkg/diff/semver.go` and the deleted `pkg/constraint` package both replaced by [`hashicorp/go-version`](https://github.com/hashicorp/go-version)** — the same library Terraform itself uses for `required_version`, module `version` constraints, and registry version selection. Two follow-on behaviour changes:
  - **Prerelease ordering** is now SemVer 2.0.0 §11-compliant: `1.0.0-beta < 1.0.0`, `>= 2.0.0-rc` is correctly broader than `>= 2.0.0`, etc. Previously prereleases were stripped during parsing and treated as equal to their release counterpart. Aligns with Terraform exactly. Build metadata is still stripped per §10.
  - **4+-segment versions** (Maven-style `1.2.3.4`) are now explicitly rejected. go-version accepts them, but tflens's `~>` interval logic only defines behaviour for 1/2/3 components.
- **`$TFE_TOKENS_FILE` renamed to `$TFLENS_TFE_TOKENS_FILE`.** The env var is namespaced to make the source unambiguous in environments where multiple tools may share TFE-style tokens. **Action required:** if you exported the old name in a shell profile or CI config, rename it. The Go constant `resolver.TfeTokensFileEnv` keeps the same name — only the underlying string changed.
- **Tracked-attribute Informational messages now explain *why* a change wasn't flagged Breaking** when static evaluation suppressed the value-change details. New phrasings: `"text changes collapsed: same effective value"` (when supporting refs are also surfaced), `"effective value unchanged: underlying text differs but evaluates to the same constant"` (when nothing else changed), and `"tracked attribute texts changed but evaluate to the same constant (no effective value change)"` (when both old and new have the marker and every diff was eval-equivalent).

### Added

- **Automated releases on PR merge.** Adding a `release:patch`, `release:minor`, or `release:major` label to a PR before merging triggers `.github/workflows/auto-release.yml`, which computes the next version, promotes `[Unreleased]` to a versioned section, tags the commit, and creates a GitHub Release with the new section as the body. PRs without a release label are silently skipped — `[Unreleased]` entries accumulate until the next release-labelled merge.
- **CHANGELOG-check CI workflow** (`.github/workflows/changelog-check.yml`) that fails a PR if user-visible code changed without a `CHANGELOG.md` update. Auto-skips when all changed files are tests / testdata / workflows / scripts / top-level docs. Explicit opt-out via the `no-changelog` label for refactors, dep bumps, or anything else genuinely non-user-visible.
- **`scripts/release.sh` + `make release` / `make release-push` targets** for the manual release path (run from a maintainer's checkout when cutting a release that bundles already-merged PRs).
- **`.github/workflows/release.yml`** that fires on manually-pushed `vX.Y.Z` tags and creates the matching GitHub Release.
- `SECURITY.md` defining the reporting channel (GitHub private security advisories), supported-versions policy, and scope (credential leakage, path traversal, parser DoS in scope; Terraform itself and hostile git-source fetches out).
- `CONTRIBUTING.md` covering scope/philosophy, dev setup, the table-driven testdata pattern (single-module + cross-module layouts), commit message convention, the release flow (both automated and manual paths), and a package map.
- `pkg/diff/testdata/cross_module_tracked/` testdata layout that mirrors a real Terraform project (`<case>/<old|new>/main.tf` + `<case>/<old|new>/modules/<call>/main.tf`). Three cases lock down the cross-module marker behaviour: `parent_change_real` (var.upgrade=true → Breaking), `parent_change_eval_unchanged` (var.upgrade=false → Informational, locks down the false-positive fix), `no_parent_change` (sanity).

### Removed

- `pkg/constraint` package (~290 LOC + tests). Functionality moved to direct `hashicorp/go-version` calls in `pkg/resolver/registry.go`.

## [0.1.3] — 2026-04-24

### Added

- **Cross-module resolution for tracked attributes.** A marker on `var.cluster_version` inside a child module now climbs through the parent's `module "<name>" { cluster_version = ... }` argument and walks any locals/vars it transitively references. Parent-side refs are surfaced under `parent.` prefixes so reviewers can see which side of the boundary moved.
- **Static evaluation suppresses no-op text changes.** When two text-different expressions evaluate to the same `cty.Value` (e.g. `"1.34"` vs `var.upgrade ? "1.35" : "1.34"` with `var.upgrade = false` → both yield `"1.34"`), the value-change detail is suppressed and the diff demotes to Informational. Distinguishes ref-existence reorganisations (Informational supporting context) from actual value changes (Breaking + CI gate). Falls back conservatively to text comparison when expressions reference unevaluable constructs (data sources, computed resource attributes, Terraform-specific functions like `length`, `contains`, `keys`, `merge`).

### Fixed

- **`LookupAttrText` now reports entity existence rather than value presence.** A variable that exists with no default returns `("", true)` instead of `("", false)`, preventing `"now references variable.X = <unset>"` false positives when the variable was already there. Marker-added cases against existing-but-undefaulted variables now correctly emit Informational.

## [0.1.2] — 2026-04-24

### Added

- **Markers inside `locals { }` blocks** are now bound to the local as its own entity (`local.<name>.value`, mirroring the output convention). Authors can annotate the source-of-truth declaration directly rather than every consumer.
- **Marker added + value also moved in same PR is now Breaking.** Previously emitted Informational ("marker added") regardless of whether the underlying value moved. Now consults the old entity's attribute text — and, for resource attributes (whose individual attrs aren't cached on `Entity`), each transitively-referenced var/local — and promotes to Breaking when any underlying value changed. Surfaces `value <old> → <new>` in the detail so the reviewer sees what shifted.

### Fixed

- **TFE tokens YAML: strip default ports when matching credentials.** A `tokens.yaml` entry written as `https://tfe.example.com:443` produced a key of `tfe.example.com:443`, while requests arrived as bare `tfe.example.com` — silent 401s on private TFE registries that included the port in their addresses. Normalisation now strips `:443` for `https` and `:80` for `http` on both sides; non-default ports preserved.

## [0.1.1] — 2026-04-24

### Added

- **`tflens diff` covers the root module.** Previously only child module calls were paired by `pairModuleCalls`; the root went undetected. Adding a required root variable, removing a root output, changing the backend, etc. now show up under a `Root module:` section. Output JSON gets a `root_changes` field.
- **Cross-module input/output validation now follows object-field traversals.** `var.config.property` against `variable "config" { type = object({property = number}) }` now correctly resolves to `number` instead of being misreported as an object/number type mismatch. Also handles map-style `m.k` access (HCL2 treats `m.k` as `m["k"]` for maps).
- **TFE tokens via `$TFE_TOKENS_FILE`** (since renamed to `$TFLENS_TFE_TOKENS_FILE` in 0.1.4). YAML format used by some Terraform Enterprise org-management tooling. Strictly opt-in: with the env var unset, the loader returns empty credentials and never touches the filesystem. Address may be a bare host, `host:port`, or a full URL.

## [0.1.0] — 2026-04-24

First tagged release of tflens — a static Terraform analyser focused on breaking-change detection across module versions.

### Highlights

- **`hcl/v2`-backed parser** (matches Terraform's own front-end), replacing the original hand-rolled lexer + parser.
- **`cty.Convert`-driven type compatibility classification.** Type widening / narrowing on variables, output type narrowing, `for_each` key-type narrowing.
- **Source-type-aware diff.** Local children get the consumer view (cross-validate parent vs new child; only consumption errors are Breaking); registry/git children get the full API diff (publisher's release contract).
- **`# tflens:track` marker** for opting specific resource attributes (engine versions, force-new names like `cluster_name`, instance classes, …) into the diff. The indirection walker follows `var.X` / `local.X` references one or two hops deep, including string interpolation. Removing the marker is itself flagged as Breaking.
- **`whatif`** — consumer-view `diff`: cross-validates the parent's argument set + output references against the candidate child; only flags changes that affect this specific caller.
- **`statediff`** — state-level hazard detection: resource adds/removes vs base ref, plus locals whose value changed and whose dependency chain reaches `count` / `for_each`. With `--state`, lists the state instances that may be affected.
- **Semver-aware version constraint comparison** (Equal / Broadened / Narrowed / Overlap / Disjoint) for module `version`, `required_version`, and provider `required_providers` constraints.
- **Fix hints** on Breaking changes with the conventional fix (e.g. required-variable-added → suggest `default = ...`, resource removed → suggest `removed {}` block, backend changes → `terraform init -migrate-state`).
- **Private registry credentials** from `~/.terraformrc` (`$TF_CLI_CONFIG_FILE`, `%APPDATA%\terraform.rc` on Windows). Tokens are sent only to host-exact matches — never leaked across redirects to a third-party CDN.

[Unreleased]: https://github.com/dgr237/tflens/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/dgr237/tflens/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/dgr237/tflens/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/dgr237/tflens/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/dgr237/tflens/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/dgr237/tflens/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/dgr237/tflens/releases/tag/v0.1.0
