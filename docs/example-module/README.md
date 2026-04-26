# Example module — dogfood target

A tiny VPC + subnet shape that the [dogfood workflow](../../.github/workflows/dogfood.yml) runs `tflens` against on every PR. Validates the [GitHub Action wrapper](../../README.md#github-action) end-to-end (sticky comments, step summary, exit code, build pipeline) on real GitHub-hosted runners.

Also doubles as a public reference example showing the patterns the per-command docs reference:

- A **tracked attribute** — `# tflens:track` on `aws_vpc.main.cidr_block`. Any change to the literal or the chain it walks (`var.vpc_cidr`'s default) is flagged Breaking by [`tflens diff`](../commands/diff.md).
- A **count-driven resource** — `aws_subnet.public` with `count = var.subnet_count`. Changing the variable's default would silently shrink the subnet count; [`tflens statediff`](../commands/statediff.md) flags it without a marker because count/for_each chains are checked automatically.
- **Static evaluation** — `merge(var.tags, { Module = "..." })` exercises the curated stdlib (see [main README — Static evaluation surface](../../README.md#static-evaluation-surface)).
- **Variables, outputs, locals, splat expressions** — the basic shape every module has.

## Why no `provider "aws" { ... }` block?

tflens is schema-free — it doesn't load provider binaries, doesn't talk to AWS, doesn't need credentials. So the analysis runs without any provider declared. To `terraform plan` or `apply` this for real, add a provider block of your own:

```hcl
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

provider "aws" {
  region = "us-east-1"
}
```

## Running tflens against it locally

```bash
tflens validate ./docs/example-module
tflens diff --ref main ./docs/example-module
tflens statediff --ref main ./docs/example-module
```

The dogfood workflow does the same thing on every PR — see the workflow file for the action invocation.
