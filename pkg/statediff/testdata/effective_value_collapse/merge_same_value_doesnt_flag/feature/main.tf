locals {
  # merge() of the same key/value pairs as main, just split across
  # two argument objects. Effective object is identical → no flag.
  tags = merge({ "env" = "prod" }, { "team" = "platform" })
}

resource "aws_instance" "web" {
  for_each = local.tags
}
