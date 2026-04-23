variable "env" { type = string }
variable "count" { type = number }

locals {
  prefix  = "${var.env}-app"
  is_prod = var.env == "prod"
}

data "aws_ami" "ubuntu" { most_recent = true }

resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
resource "aws_instance" "web" { ami = data.aws_ami.ubuntu.id }

output "vpc_id" { value = aws_vpc.main.id }
