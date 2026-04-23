variable "env" {}
resource "aws_vpc" "main" { tags = { Env = var.env } }
