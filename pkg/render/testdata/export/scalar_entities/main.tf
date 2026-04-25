variable "region" {
  type    = string
  default = "us-east-1"
}

variable "instance_count" {
  type      = number
  sensitive = true
}

locals {
  image    = format("ec2-%s-v%d", "small", 3) # tflens:track: AMI image identifier
  unevaled = data.aws_ami.latest.id           # references a data source — not evaluable
}

data "aws_ami" "latest" {
  most_recent = true
}

output "image" {
  value = local.image
}

output "instance_count" {
  value     = var.instance_count
  sensitive = true
}
