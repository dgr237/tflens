locals {
  image = "ec2-small-v3"
}

resource "aws_instance" "web" {
  for_each = toset([local.image])
}
