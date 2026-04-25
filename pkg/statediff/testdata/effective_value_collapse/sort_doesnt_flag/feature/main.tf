locals {
  # Same effective value as main — distinct() of an already-distinct
  # tuple returns the same tuple. Text differs; value doesn't.
  regions = distinct(["us-east-1", "us-west-2"])
}

resource "aws_instance" "web" {
  for_each = toset(local.regions)
}
