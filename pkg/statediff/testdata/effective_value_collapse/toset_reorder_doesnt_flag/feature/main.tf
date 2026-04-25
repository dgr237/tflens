locals {
  # Source list is reordered + has a duplicate; toset() folds both
  # away. Effective set is the same as main → no SensitiveChange.
  ids = toset(["c", "a", "b", "a"])
}

resource "aws_instance" "web" {
  for_each = local.ids
}
