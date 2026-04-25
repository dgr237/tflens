locals {
  # Refactor pushed the canonical form through lower(); effective
  # string identical → no SensitiveChange even though the local
  # reaches for_each.
  region = lower("US-EAST-1")
}

resource "aws_instance" "web" {
  for_each = toset([local.region])
}
