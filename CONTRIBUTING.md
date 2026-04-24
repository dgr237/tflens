# Contributing to tflens

Thanks for your interest. tflens is a focused tool — static Terraform analysis, no Terraform execution, no provider schemas — and contributions that fit that scope are very welcome.

This file covers how to get a change from idea to merged commit. For *what* tflens does and the feature surface, see the [README](README.md).

## Scope and philosophy

Before investing time, a few things to know about what we do and don't take:

- **Static only.** If a feature needs to call `terraform plan`, query a provider, or embed a provider schema, it's out of scope. That's `terraform validate`'s / `terraform plan`'s job, and tflens is explicitly a complement, not a replacement. See [Fundamental limitations](README.md#fundamental-limitations).
- **Precision over noise.** Every classification (Breaking / NonBreaking / Informational) is load-bearing — a Breaking that fires on something safe is a worse bug than a missing detection. If you're adding a new detection, include the reasoning for the classification in the commit message.
- **Conservative defaults.** If tflens can't *prove* something is safe, flag it. Graceful-degradation paths should err on the side of the stricter verdict (e.g. unevaluable expressions fall back to text comparison and report Breaking when the text differs).
- **One concept per PR.** Unrelated cleanups in the same PR slow review. Split them.

For bigger changes, open an issue to discuss the approach before coding. Small fixes (a missed classification, a wrong hint, a test for an edge case) can go straight to a PR.

## Development setup

Requires:

- Go 1.25 or newer — see `go.mod`
- `git` on `$PATH` — `diff`, `whatif`, and `statediff` use `git worktree`

```bash
git clone https://github.com/dgr237/tflens.git
cd tflens
make check   # vet + fmt-check + test
```

Common make targets:

| Target           | Purpose                                      |
| ---------------- | -------------------------------------------- |
| `make build`     | Build `./tflens`                             |
| `make install`   | Install into `$(go env GOPATH)/bin`          |
| `make test`      | Run all tests                                |
| `make check`     | `vet` + `fmt-check` + `test` (pre-commit)    |
| `make coverage`  | Produce `coverage.html`                      |

Without make: `go build -o tflens .` and `go test ./...` work fine.

## Code style

- `gofmt` is enforced by `make check`. The pre-push flow is: `make check` → `git commit`.
- Comments: explain *why*, not *what*. Well-named identifiers and surrounding code convey the *what*. See existing files for the tone — terse, specific, no narration.
- Keep exported API surfaces small. If something doesn't need to be exported, it shouldn't be.
- Errors: wrap with `%w` and include the operation + operand. `fmt.Errorf("discovering %s: %w", host, err)` over `fmt.Errorf("discovery failed")`.
- Security: credential loaders (`LoadTerraformrc`, `LoadTfeTokens`) must fail closed — a malformed config yields a warning and an empty source, never a random partial state.

## Tests

Every behavioural change needs a test. For regressions, the test goes in first and should fail on `main`, then be fixed by the same commit (or a follow-up with a clear reference).

### Table-driven tests with testdata

Most packages use a table-driven pattern with per-case `.tf` fixtures under `testdata/`:

```
pkg/diff/testdata/
  <case_name>/
    old.tf
    new.tf
```

The test function iterates a `[]caseStruct` and runs `t.Run(tc.Name, ...)` for each. Adding a case is:

1. Drop `old.tf` and `new.tf` into a new subdirectory
2. Append an entry to the cases slice in the test file
3. `go test ./pkg/diff/...`

Cross-module fixtures (under `pkg/diff/testdata/cross_module_tracked/`) use the real Terraform project layout:

```
<case_name>/
  old/
    main.tf                         # parent / root
    modules/<call>/main.tf          # child — where the marker lives
  new/
    main.tf
    modules/<call>/main.tf
```

### Inline Go tests

Keep inline only when the case genuinely doesn't benefit from .tf files — e.g. testing helper functions, sanity checks, or assertions about nil handling.

### CI

CI runs on Linux, macOS, and Windows (Go 1.25). All three must pass before a PR merges. Windows occasionally hits `actions/setup-go` cache flakes — rerun the job before investigating. A repeated failure on one OS is a real issue.

## Commit messages

Format: `<scope>: <imperative summary under ~72 chars>` followed by a blank line and a wrapped body that explains *why* the change is needed.

Scopes that appear frequently: `analysis`, `diff`, `tracked`, `loader`, `resolver`, `cmd/diff`, `cmd/whatif`, `cmd/statediff`, `README`, `test`.

Example:

```
tracked: don't false-flag refs that existed in old without a default

LookupAttrText returned ("", false) for a variable with no default,
making refValueInModule unable to distinguish "variable existed in
old but had no default" from "variable didn't exist at all". The
diff's marker-added path then misreported the unchanged variable as
"now references variable.X = <unset>" and incorrectly promoted to
Breaking.

Make the bool report entity/attribute existence, not value presence:
a variable that exists with no default returns ("", true). The diff
then sees old "" == new "" and emits no detail, falling through to
Informational "marker added" only — which is correct, since nothing
actually changed.
```

Describe the incident / failure mode, the root cause, and the fix. Include the user-visible change (what a reviewer would see differently) when relevant.

## Releases

Two paths, both ending in a tagged GitHub Release with the relevant CHANGELOG section as the body.

### Path 1 — Automated, on PR merge (preferred)

Add a `release:patch`, `release:minor`, or `release:major` label to a PR before merging. When the PR lands on `main`, `.github/workflows/auto-release.yml`:

1. Reads the label (highest bump wins if multiple are set)
2. Computes the next version from the latest existing `vX.Y.Z` tag
3. Runs `scripts/release.sh` to promote `## [Unreleased]` → `## [vX.Y.Z]` and tag the commit
4. Creates the GitHub Release with the new section as the body

**PRs without a release label are silently skipped** — `[Unreleased]` entries accumulate until the next release-labelled PR triggers a bump.

### Path 2 — Manual via GitHub UI

For releases that aren't tied to a single PR (e.g. cutting a release that bundles several already-merged PRs), or to retry a failed auto-release:

1. Go to **Actions** → **"Auto-release"** → **"Run workflow"**
2. Either pick a `bump` type (default: `patch`) or enter an explicit `version` (`X.Y.Z`). If both are supplied, `version` wins.
3. Confirm

The same flow as the PR-merge path runs: CHANGELOG promoted, commit + tag created, GitHub Release published.

### Path 3 — Manual from a maintainer's checkout

```bash
git checkout main && git pull
make release-push VERSION=X.Y.Z
```

`scripts/release.sh`:

- Verifies preconditions (on `main`, clean tree, tag doesn't exist, `[Unreleased]` non-empty)
- Promotes the CHANGELOG, commits, tags, pushes
- The tag-push triggers `.github/workflows/release.yml` which creates the GitHub Release

Run `make release VERSION=X.Y.Z` (without `-push`) first if you want to inspect the commit and tag before publishing.

### Why two workflows?

`auto-release.yml` does its own GitHub Release creation inline because tags pushed by `GITHUB_TOKEN` don't trigger downstream workflows (the well-known recursion guard). `release.yml` still handles Path 3, where the maintainer pushes the tag from their own credentials, which DO trigger workflows.

Both PR-merge and `workflow_dispatch` paths go through `auto-release.yml`; pick whichever fits the situation.

If you ever need to redo a release (typo in the changelog, etc.):

```bash
git tag -d vX.Y.Z                          # local
git push origin :refs/tags/vX.Y.Z          # remote
git reset --hard <commit-before-release>   # rewind main
git push --force-with-lease origin main    # publish (rare!)
make release-push VERSION=X.Y.Z            # re-do
```

## Pull requests

1. **Branch** off `main`. Name it descriptively (`fix-eval-no-default`, not `patch-1`).
2. **Run `make check`** before pushing.
3. **Open the PR** against `main`. Include:
   - A summary of the change
   - A test plan (what you ran manually; which tests now cover the scenario)
   - Any README updates alongside the code change — docs and code move together
   - A `CHANGELOG.md` entry under `## [Unreleased]` for any user-visible change (added behaviour, changed semantics, removed feature, fixed bug, security note). Internal refactors and pure test additions don't need one — apply the `no-changelog` label if the PR's diff is non-trivial but the user-facing surface is unchanged. The changelog-check CI workflow auto-skips PRs whose only changed files fall under `*_test.go` / `*/testdata/*` / `.github/*` / `scripts/*` / top-level `.md`, so no label needed for those.
4. **Wait for CI.** All three OS jobs must pass.
5. **One reviewer approval** is sufficient for routine changes; architectural shifts may need more discussion.

Do NOT:

- Force-push after review has started (use fixup commits instead)
- Bypass hooks (`--no-verify`) without an explicit reason in the PR
- Land changes that break one OS even if the others pass — Windows path handling and symlink semantics catch real bugs

## Package map

Quick tour for first-time contributors:

| Package          | Responsibility                                                                 |
| ---------------- | ------------------------------------------------------------------------------ |
| `pkg/token`      | Source-position type (thin wrapper over `hcl.Pos`)                             |
| `pkg/analysis`   | Entity inventory, dependency graph, type system, tracked-attribute scanner     |
| `pkg/loader`     | Multi-file / directory / recursive module loading via `hclparse`               |
| `pkg/diff`       | Two-module API comparison + tracked-attribute diff + `DiffTrackedCtx` cross-module + version-constraint relation classification (broadened/narrowed/disjoint) |
| `pkg/resolver`   | Module source resolver chain (local → manifest → registry → git) + credentials |
| `pkg/cache`      | Content-addressable disk cache for downloaded module sources                   |
| `pkg/tfstate`    | Terraform state file reader (used by `statediff`)                              |
| `cmd/`           | Cobra command wiring                                                           |

## Questions

Open a GitHub issue with the `question` label. Security-sensitive questions should go through the [security policy](SECURITY.md) instead.
