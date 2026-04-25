locals {
  regions = ["us-east-1", "us-west-2"]
}

resource "aws_instance" "web" {
  for_each = toset(local.regions)
}
