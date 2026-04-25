## `tflens statediff` results

**3 flagged items.**

### Added resources

- `aws_s3_bucket.logs`

### Removed resources

- `aws_iam_user.old`

### Renamed resources (via `moved` blocks)

- `aws_vpc.main` → `aws_vpc.primary`

### Sensitive changes reaching count/for_each

<details><summary><code>local.regions</code> changed (`["us-east-1"]` → `["us-east-1","us-west-2"]`)</summary>

Affected resources:

- `aws_subnet.per_region` (via `for_each`)
  - state instance: `aws_subnet.per_region["us-east-1"]`

</details>

### State orphans (in state but not in source)

- `aws_security_group.legacy`

