# Command reference

Per-command documentation for `tflens`. New to the tool? Start with [Getting started](../getting-started.md).

## Comparison commands (work against a git ref)

| Command | View | Question it answers |
|---|---|---|
| [`diff`](diff.md) | Author | What changed in the module's API between this branch and the base ref? |
| [`whatif`](whatif.md) | Consumer | If I merged this, would my parent still work? |
| [`statediff`](statediff.md) | Operator | If I merge this, which of my state's resource instances are at risk? |

## Single-tree commands

| Command | Question it answers |
|---|---|
| [`validate`](validate.md) | Are there undefined references, type errors, sensitive-value leaks, or `for_each` / `count` misuse? |

## Concepts

| Concept | What it does |
|---|---|
| [`# tflens:track` markers](tracked-attributes.md) | Opt specific resource attributes into the `diff` output — the source-only alternative to `--enrich-with-plan` for module-developer CI. |

## Experimental

| Command | What it does |
|---|---|
| [`export`](export.md) | Emit the enriched module model as JSON. Building block for converters to other provisioning systems (kro, Crossplane, Pulumi, …). Shape is versioned and explicitly experimental. |

## Utility commands

These are documented inline in the [main README's Commands table](../../README.md#commands) — the one-line description is sufficient. They don't have dedicated reference pages because their behaviour is a thin wrapper over the analysis primitives.

- `inventory` — list every declared entity with source positions
- `deps` / `impact` — what does X depend on / what's affected if X changes
- `unused` — entities nothing else references
- `cycles` — dependency cycles
- `graph` — Graphviz DOT output of the dependency graph
- `fmt` — `hclwrite.Format` over a single file
- `cache info` / `cache clear` — manage the downloaded-module cache

## Plan enrichment

Three of the comparison commands take an optional `--enrich-with-plan plan.json` flag that folds `terraform show -json` output into the analysis:

- [`diff --enrich-with-plan`](diff.md#plan-enrichment) — attribute-level deltas for the root module + every paired call.
- [`whatif --enrich-with-plan`](whatif.md#plan-enrichment) — plan-derived findings layered onto each call's API-changes section.
- [`statediff --enrich-with-plan`](statediff.md#plan-enrichment) — pairs the static "this count expression COULD recompute" signal with the plan's "here are the N concrete instances that WILL be affected."

Plan enrichment is best for **consumer-side CI** where a real plan exists. For **module-developer CI** the source-only [`# tflens:track`](tracked-attributes.md) marker is the right tool — no plan, no credentials, no consuming workspace.

## Output formats

Every command supports three output formats via the global `--format` flag:

- **text** (default) — terse, greppable.
- **`--format json`** — structured envelope for downstream tooling. Warnings stay on stderr so stdout is pipeable.
- **`--format markdown`** — GitHub-flavoured markdown with severity badges (🔴🟡🔵), collapsible sections, fix hints. Designed for sticky PR comments and `$GITHUB_STEP_SUMMARY` — pipe directly to `gh pr comment` or use the [GitHub Action](../../README.md#github-action) for sticky-comment plumbing.
