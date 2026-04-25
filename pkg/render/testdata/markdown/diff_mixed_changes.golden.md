## `tflens diff` results

_Base ref: `main` &middot; Path: `./infra`_

**Summary:** 2 🔴 Breaking, 1 🟡 Non-breaking, 1 🔵 Informational

## Root module

- 🔴 Breaking &mdash; `variable.cluster_name`: required variable added
  > **Fix:** add `cluster_name = ...` to the root invocation


<details open><summary>Module <code>vpc</code> &mdash; version <code>1.0.0</code> → <code>2.0.0</code></summary>

- 🔴 Breaking &mdash; `var.required`: removed
  > **Fix:** callers passing this variable will fail
- 🟡 Non-breaking &mdash; `var.tags`: added optional
- 🔵 Informational &mdash; `out.docs`: description updated

</details>

### Module `eks` &mdash; **ADDED** (source `terraform-aws-modules/eks/aws`, version `20.0.0`)

