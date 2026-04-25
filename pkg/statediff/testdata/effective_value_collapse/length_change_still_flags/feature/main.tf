locals {
  # The set ACTUALLY changes — "c" added. Must still be flagged
  # despite text-vs-value ambiguity in other parts of the
  # expression. Pin the true-positive case so the new value-
  # equality short-circuit doesn't over-suppress.
  ids = toset(["a", "b", "c"])
}

resource "aws_instance" "web" {
  for_each = local.ids
}
