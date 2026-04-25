locals {
  region = "us-east-1"
}

resource "aws_instance" "web" {
  for_each = toset([local.region])
}
