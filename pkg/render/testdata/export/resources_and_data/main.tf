variable "regions" {
  type = list(string)
}

resource "aws_instance" "web" {
  for_each = toset(var.regions)
  lifecycle {
    prevent_destroy = true
    ignore_changes  = [tags]
  }
}

resource "aws_security_group" "sg" {
  count = 2
}

data "aws_ami" "ubuntu" {
  most_recent = true
}

data "aws_caller_identity" "current" {}
