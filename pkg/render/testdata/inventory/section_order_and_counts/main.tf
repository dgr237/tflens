variable "v" {
  type = string
}

locals {
  l = 1
}

data "aws_ami" "u" {}

resource "aws_vpc" "main" {}

module "m" {
  source = "./x"
}

output "o" {
  value = 1
}
