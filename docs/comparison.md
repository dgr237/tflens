# tflens vs other Terraform tools

Most tools in the Terraform ecosystem solve a different problem from tflens. This page positions tflens against the best-known alternatives so you can decide what to run alongside it (the answer is usually "tflens AND one of these," not "tflens INSTEAD OF").

## TL;DR

| Question | Best tool |
|---|---|
| Is my code idiomatic and provider-correct? | **TFLint** |
| Does my code violate security / compliance policy? | **Checkov**, **Terrascan**, **tfsec / Trivy** |
| Is my Terraform syntactically and semantically valid? | **`terraform validate`** (official) |
| What will Terraform actually do when I apply this? | **`terraform plan`** (official) |
| **Did I just break consumers of my module?** | **tflens** |
| **Will this PR silently destroy production state?** | **tflens** |
| **Is the SemVer bump on this module release accurate?** | **tflens** |

tflens addresses a problem the others don't: **change impact between two refs**, classified for SemVer / CI gating. The others operate on a single tree at a single point in time.

## Where tflens fits in the landscape

```
                    Single point in time              Across two refs
                    ───────────────────────           ───────────────────────
Language correctness  terraform validate                — (tflens compares,
                      tflens validate                     doesn't lint a snapshot)
                      TFLint                            
                                                       
Security posture      Checkov, Terrascan, tfsec        — (out of scope; static
                      OPA / Conftest                      posture is orthogonal)
                                                       
Behaviour preview     terraform plan                   — (plan is point-in-time;
                                                          tflens enriches with plan)
                                                       
Change impact         —                                tflens (diff, whatif,
                                                          statediff)
```

The bottom-right cell — **change impact across refs** — is what tflens is built for. The others either operate on one tree (top row) or operate point-in-time (middle rows).

## tflens vs TFLint

[TFLint](https://github.com/terraform-linters/tflint) is the canonical Terraform linter. Per-provider plugins know AWS / GCP / Azure resource shapes deeply — deprecated arguments, invalid `instance_type` values, naming conventions, dead code.

**What TFLint does that tflens doesn't:**

- **Provider-rule linting.** TFLint knows `aws_instance.instance_type = "t99.huge"` is invalid; tflens doesn't (we don't embed provider schemas — see [`tracked-attributes.md` § "Why it exists"](commands/tracked-attributes.md#why-it-exists-and-why-not-just---enrich-with-plan)).
- **Style and naming conventions.** snake_case enforcement, deprecated-syntax warnings, module-block ordering.
- **Per-resource validation rules.** Customisable via `.tflint.hcl` plugin config.

**What tflens does that TFLint doesn't:**

- **Compare two refs.** TFLint runs against the current tree only. It can't tell you "this PR removed a required variable that 12 callers rely on."
- **Classify changes for CI gating.** tflens emits Breaking / Non-breaking / Informational with fix hints; TFLint emits warnings/errors with fix suggestions, but the categories are about correctness, not contract impact.
- **Semver-aware version constraint diffs.** `version = "~> 1.0"` → `"~> 2.0"` is parsed into intervals and compared.
- **Statediff** — find resources whose count/for_each will silently change instance counts based on a local/variable-default edit.
- **Cross-module input validation across the call graph.**

**Recommended pairing:** **TFLint + tflens.** TFLint catches "this code is wrong"; tflens catches "this code is different in a way callers will notice." Both run in CI on every PR; together they cover the lint + change-impact axes.

## tflens vs Checkov

[Checkov](https://github.com/bridgecrewio/checkov) is a security and compliance scanner. Massive policy library (1000+ checks for AWS, Azure, GCP, Kubernetes, Helm, Dockerfile, etc.) aligned with CIS benchmarks, SOC2, HIPAA, PCI-DSS.

**What Checkov does that tflens doesn't:**

- **Security posture checks.** "S3 bucket is publicly readable", "IAM policy grants `*:*`", "KMS key has no rotation enabled."
- **Compliance reporting.** Map findings to specific framework controls. SARIF / SOC2 / HIPAA report formats.
- **Multi-IaC coverage.** Scans Terraform, CloudFormation, Kubernetes manifests, Helm charts, Dockerfiles.

**What tflens does that Checkov doesn't:**

- **Cross-ref change detection.** Checkov is point-in-time — "is this code secure?". tflens is differential — "did the API of this module just change?".
- **Module-author tooling.** Checkov audits the deployed configuration; tflens audits the contract between module authors and consumers.
- **Plan enrichment.** Force-new attribute changes, sensitive value redaction, `(known after apply)` rendering — all surfaced in the diff.

**Recommended pairing:** **Checkov + tflens.** Checkov tells you "this S3 bucket is misconfigured." tflens tells you "this module that produces S3 buckets just dropped a required input that 12 consumers depend on." Both matter, and both run cheaply in CI.

## tflens vs Terrascan

[Terrascan](https://github.com/tenable/terrascan) is a security policy scanner with an OPA / Rego policy engine. Custom-policy-friendly, multi-cloud, supports Helm + K8s alongside Terraform.

Same general positioning as Checkov above:

- **Terrascan does:** security/compliance scanning with custom OPA policies, point-in-time.
- **tflens does:** change-impact analysis across refs, with module-author / consumer / operator views.

**Recommended pairing:** Terrascan for posture + custom org policy, tflens for change impact. They're orthogonal.

## tflens vs tfsec / Trivy

[tfsec](https://github.com/aquasecurity/tfsec) (now consolidated into [Trivy](https://github.com/aquasecurity/trivy)) is HCL-native, fast, with a large built-in security ruleset. Same problem space as Checkov / Terrascan: security posture on a single tree.

Same positioning as above. Run alongside tflens; they don't overlap.

## tflens vs `terraform validate`

The official `terraform validate` is the closest semantic neighbour to `tflens validate`. Both check that a configuration is internally consistent.

**What `terraform validate` does that `tflens validate` doesn't:**

- **Provider-aware attribute checking.** `terraform validate` runs after `terraform init`, so it knows the AWS provider's schema and can verify `aws_vpc.main.cidr_block` is a string, that required attributes are present, that argument types match.
- **Authoritative semantics.** It IS the language reference implementation — anything `terraform validate` accepts, `terraform apply` will accept (modulo runtime values).

**What `tflens validate` does that `terraform validate` doesn't:**

- **Runs in seconds with no setup.** No `terraform init`, no provider downloads (sometimes hundreds of MB), no credentials. Suitable for pre-commit hooks and per-file editor lints.
- **Cross-module input validation.** A `module "x" { source = "..." cidir = "..." }` typo is flagged at the call site even though Terraform itself wouldn't notice until plan time.
- **Sensitive-value propagation tracking.** An output that exposes a sensitive variable but isn't itself marked `sensitive` is flagged before plan.
- **Type errors on variable defaults** even when the default is never referenced.

**Recommended pairing:** Use **`tflens validate`** at PR review time / pre-commit (cheap, fast, catches the cross-module + propagation issues). Use **`terraform validate`** before deploy (heavier, but provider-aware).

## tflens vs `terraform plan`

The gold standard for "what will actually happen." Fully evaluated, all references resolved, all attribute values computed against the live provider schema and current state.

**What `terraform plan` does that tflens doesn't:**

- **Concrete behaviour preview.** Force-new attribute changes, computed value resolution, provider-side validation rules — all visible.
- **State-aware.** Knows what currently exists and what would change.
- **Authoritative.** What plan says, apply does (modulo race conditions).

**What tflens does that `terraform plan` doesn't:**

- **Runs without credentials, without state, without a workspace.** Module developers don't HAVE the consumer's plan — `terraform plan` requires the consuming workspace + credentials + a real state to operate against. tflens runs from the module repo against any git ref.
- **Compares two refs.** `terraform plan` shows the next apply. tflens shows the API delta between branches.
- **Catches API contract changes invisible to plan.** A removed module variable doesn't affect your plan if you don't pass that variable. But it WILL affect every other consumer who does. tflens flags it; plan doesn't.
- **Sub-second runtime.** Per-PR friendly. Plan typically takes minutes (provider download + state refresh + remote queries).

**Recommended pairing:** **tflens at PR-review time + terraform plan at deploy time.** tflens is the cheap, every-PR safety net; plan is the deliberate, pre-apply gate. They answer different questions.

`tflens diff --enrich-with-plan plan.json` brings the two together: when you DO have a plan, fold its attribute-level deltas into the static-analysis findings. See [`docs/commands/diff.md` § "Plan enrichment"](commands/diff.md#plan-enrichment).

## tflens vs Atlantis / Terragrunt

[Atlantis](https://www.runatlantis.io/) and [Terragrunt](https://terragrunt.gruntwork.io/) are workflow orchestrators rather than analysers — they run `terraform plan` and `terraform apply` on PRs, manage workspaces, etc. They sit one layer up from tflens.

You'd run tflens INSIDE an Atlantis or Terragrunt workflow as one of the gates, not instead of them.

## When to use which (decision matrix)

| You want to know… | Run |
|---|---|
| Is this code idiomatic and provider-correct? | TFLint |
| Does this code violate security policy? | Checkov / Terrascan / tfsec |
| Is this Terraform internally consistent (with full provider awareness)? | `terraform validate` |
| Is this Terraform internally consistent (fast, schema-free)? | `tflens validate` |
| What will Terraform actually do at apply? | `terraform plan` |
| Did I break consumers of my module? | `tflens diff` |
| Would my parent module break under this child upgrade? | `tflens whatif` |
| Will this PR add, destroy, or re-instance state resources? | `tflens statediff` |
| Are critical attributes (engine versions, instance classes, force-new) being silently changed? | `tflens diff` with [`# tflens:track`](commands/tracked-attributes.md) markers |
| Is the SemVer bump on this module release accurate? | `tflens diff` (Breaking count → major bump) |

## Common CI stacks

### Module repo (publishing a reusable module)

```yaml
- name: Lint
  run: tflint --recursive
- name: Security posture
  run: checkov -d . --quiet
- name: Static validation
  run: tflens validate .
- name: Change impact (gate the merge)
  uses: dgr237/tflens@v0.15.0
  with:
    command: diff
    ref: origin/${{ github.base_ref }}
```

The first three catch common defects on the current tree. tflens diff catches "did this PR break the API" — the question only tflens answers.

### Workspace repo (consuming modules)

```yaml
- name: Lint + security
  run: |
    tflint --recursive
    checkov -d . --quiet

- name: Plan
  run: |
    terraform init
    terraform plan -out=tfplan
    terraform show -json tfplan > plan.json

- name: Whatif (consumer-side change impact)
  uses: dgr237/tflens@v0.15.0
  with:
    command: whatif
    ref: origin/${{ github.base_ref }}
    plan: plan.json

- name: Statediff (state hazard with plan corroboration)
  uses: dgr237/tflens@v0.15.0
  with:
    command: statediff
    ref: origin/${{ github.base_ref }}
    plan: plan.json
    state: terraform.tfstate
```

`whatif` flags upgrades that break THIS workspace's usage; `statediff` flags PRs that may destroy production state. Both gain attribute-level fidelity from `--enrich-with-plan`.

## What tflens deliberately doesn't try to do

To avoid duplicating the work that the tools above do well:

- **No security posture checks.** Use Checkov / Terrascan / tfsec.
- **No provider-rule linting.** Use TFLint.
- **No plan replacement.** Use `terraform plan` for the authoritative behaviour preview.
- **No drift detection** between state and reality. Use `terraform refresh` / drift-management tooling.
- **No multi-IaC coverage.** Terraform / OpenTofu only.

These boundaries keep tflens fast, focused, and easy to wire alongside the security and linting layers your CI probably already has.

## Related

- **[main README](../README.md)** — install + GitHub Action wrapper.
- **[Getting started](getting-started.md)** — 5-minute walkthrough.
- **[`docs/commands/`](commands/)** — full per-command reference.
