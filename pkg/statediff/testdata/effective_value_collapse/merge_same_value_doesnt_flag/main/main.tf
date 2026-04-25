locals {
  tags = { "env" = "prod", "team" = "platform" }
}

resource "aws_instance" "web" {
  for_each = local.tags
}
