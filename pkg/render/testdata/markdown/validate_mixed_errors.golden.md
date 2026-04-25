## `tflens validate` results

**3 issues found.** 🔴

### Undefined references

- `resource.aws_vpc.main` references undeclared `var.typo` &mdash; `main.tf:12`

### Cross-module issues

- `module.vpc`: child requires `region` but root passes `regions` &mdash; `main.tf:5`

### Type errors

- `variable.count` (`default`): default value `"three"` is string, declared type number &mdash; `variables.tf:3`

