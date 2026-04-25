locals {
  ids = toset(["a", "b"])
}

resource "aws_instance" "web" {
  for_each = local.ids
}
