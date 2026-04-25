locals {
  ids = toset(["a", "b", "c"])
}

resource "aws_instance" "web" {
  for_each = local.ids
}
