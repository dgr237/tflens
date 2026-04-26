# Getting started with tflens

A 5-minute tour. We'll install the binary, run the four most useful subcommands against a tiny sample module, and wire up CI gating.

## Install

```bash
go install github.com/dgr237/tflens@latest
# or, from a checkout:
make install
```

Requires Go 1.25+. For network-mode module resolution (registry / git sources), `git` must be on `$PATH`.

## A sample module

Create a fresh directory with one file:

```bash
mkdir -p ~/tflens-tour && cd ~/tflens-tour
git init -q && git commit --allow-empty -m "init"
```

```hcl
# main.tf
variable "vpc_cidr" {
  type    = string
  default = "10.0.0.0/16"
}

resource "aws_vpc" "main" {
  cidr_block = var.vpc_cidr
}

output "vpc_id" {
  value = aws_vpc.main.id
}
```

Commit it as the baseline:

```bash
git add main.tf && git commit -m "baseline"
```

## 1. `validate` — find broken references

`validate` checks for undefined references, type mismatches, sensitive-value leaks, and `for_each`/`count` misuse. Edit `main.tf` to introduce a typo:

```hcl
output "vpc_id" {
  value = aws_vpc.main.unknown_attr  # ← invalid attribute
}
```

```bash
tflens validate .
```

You'll get a one-line undefined-reference report with the file:line. Revert the change before continuing.

→ Full reference: [`docs/commands/validate.md`](commands/validate.md)

## 2. `diff` — see what changed in the module's API

This is the **author view**: "what changed between this branch and the base ref?". Edit `main.tf` to remove the variable's default:

```hcl
variable "vpc_cidr" {
  type = string
  # default removed
}
```

```bash
tflens diff --ref HEAD~1 .
```

```
Root module:
  Breaking (1):
    variable.vpc_cidr: default removed (variable now required)
      hint: add `default = ...` to make it optional, or document that callers must set it
```

That's the headline value: `tflens diff` classifies every API change as **Breaking** / **Non-breaking** / **Informational** with a fix hint. CI exits non-zero on any Breaking finding.

→ Full reference: [`docs/commands/diff.md`](commands/diff.md)

## 3. `# tflens:track` — opt resource attributes into the diff

Resource-attribute changes (e.g. `cluster_version = "1.28"` → `"1.29"`) are normally *not* diffed — most are noise. Add a marker to opt one in:

```hcl
resource "aws_vpc" "main" {
  cidr_block = var.vpc_cidr  # tflens:track: VPC CIDR is load-bearing
}
```

Now any change to `var.vpc_cidr`'s value will surface as a Breaking finding against `cidr_block`. The marker description appears as the hint. This is the source-only alternative to `--enrich-with-plan`; it doesn't need a plan, credentials, or any consuming workspace.

→ Full reference: [`docs/commands/tracked-attributes.md`](commands/tracked-attributes.md)

## 4. `statediff` — find changes that may destroy state

`statediff` flags resource adds/removes vs. the base ref, plus locals or variable defaults whose value changed AND whose dependency chain reaches `count` / `for_each` (the silent class of bug where editing a list quietly destroys instances).

Add a `count` resource that depends on a variable, then change the default:

```hcl
variable "subnet_count" {
  type    = number
  default = 3
}

resource "aws_subnet" "public" {
  count      = var.subnet_count
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.0.${count.index}.0/24"
}
```

Commit, then change `default = 3` to `default = 1`:

```bash
tflens statediff --ref HEAD~1 .
```

```
Value changes that may alter count/for_each expansion:
  - variable.subnet_count
      old: 3
      new: 1
    Affected: aws_subnet.public (count)
```

Pair with `--enrich-with-plan` and the matched-instance addresses appear underneath ("destroys aws_subnet.public[1], aws_subnet.public[2]").

→ Full reference: [`docs/commands/statediff.md`](commands/statediff.md)

## 5. Wire it into CI

The repo ships a composite GitHub Action — one `uses:` line per workflow:

```yaml
permissions:
  contents: read
  pull-requests: write   # required for the sticky PR comment

jobs:
  tflens:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with: { fetch-depth: 0 }
      - uses: dgr237/tflens@v0.15.0
        with:
          command: diff
          ref:     origin/${{ github.base_ref }}
```

Findings get posted as a sticky PR comment (re-runs edit in place), appended to `$GITHUB_STEP_SUMMARY`, and the step exits non-zero on Breaking findings.

→ Full reference: [main README — GitHub Action](../README.md#github-action)

## Where to go next

- **[`docs/commands/`](commands/)** — full per-command reference with examples and CI patterns
- **[`docs/commands/tracked-attributes.md`](commands/tracked-attributes.md)** — the marker, the indirection rules, the cross-module flow
- **[`docs/comparison.md`](comparison.md)** — tflens vs TFLint / Checkov / Terrascan / `terraform validate` / `terraform plan`. Where each tool fits and what to run alongside.
- **[main README — Module resolution](../README.md#module-resolution)** — how `source = "..."` resolves, private registries, the cache
- **[main README — Fundamental limitations](../README.md#fundamental-limitations)** — what tflens deliberately doesn't do
