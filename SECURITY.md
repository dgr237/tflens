# Security policy

## Supported versions

Security fixes land on `main` and are released as the next patch version. Only the latest `v0.1.x` receives security updates.

| Version    | Supported |
| ---------- | --------- |
| `v0.1.x`   | ✓         |
| `< v0.1.0` | ✗         |

## Scope

tflens is a **static analyser**: it parses `.tf` files, builds an entity graph, and compares two versions. It does not execute Terraform, query providers, or run user-authored code. The security surface is therefore narrow.

**In scope** — vulnerabilities in tflens itself:

- Arbitrary code execution triggered by parsing a malicious `.tf` file
- Path traversal / arbitrary file read via crafted `source = "..."` values, git refs, or module names
- Token leakage (private-registry credentials from `~/.terraformrc`, `$TFE_TOKENS_FILE`, or `.terraform/modules/modules.json`) — e.g. tokens sent to an unintended host via a redirect or misconfigured resolver
- Denial of service (unbounded memory / CPU) triggered by crafted input
- Command injection in the `git worktree` / `git rev-parse` call sites used by `diff`, `whatif`, and `statediff`

**Out of scope:**

- Vulnerabilities in Terraform itself, in HashiCorp's `hcl` / `cty` libraries (report those upstream), or in any Terraform module being analysed
- Findings tflens correctly produces (Breaking / Informational markers are the product doing its job, not a security issue)
- Running tflens against untrusted `.tf` files that import malicious git sources — running `git clone` on a hostile URL is a risk of any git-resolving tool. Use `--offline` if you don't trust the inputs.

## Reporting a vulnerability

**Please do not open a public issue.**

Use GitHub's private security advisory flow:

<https://github.com/dgr237/tflens/security/advisories/new>

Include:

1. A description of the issue and the impact
2. Steps to reproduce (a minimal `.tf` file or command that triggers it)
3. The `tflens` version (commit SHA or tag) and the OS you observed it on

You'll get an acknowledgment within 7 days and a fix timeline within 14 days. Simple issues typically ship in the next patch release; complex ones may need a coordinated disclosure window, which we'll agree on explicitly.

## Disclosure

Once a fix is released, the advisory is made public and credit is given to the reporter unless they prefer to remain anonymous.
