variable "region" {
  description = "AWS region the stack is deployed into"
  type        = string
  default     = "us-east-1"
}

variable "instance_count" {
  type      = number
  sensitive = true
}

variable "tags" {
  type    = map(string)
  default = {}
}

variable "subnets" {
  type = list(object({
    cidr             = string
    az               = string
    public           = optional(bool, false)
    additional_cidrs = optional(list(string), [])
  }))
  default = []
}

variable "cluster" {
  type = object({
    name    = string
    version = optional(string, "1.30")
    addons = optional(object({
      coredns    = optional(bool, true)
      kube_proxy = optional(bool, true)
    }), {})
  })
}

locals {
  image    = format("ec2-%s-v%d", "small", 3) # tflens:track: AMI image identifier
  unevaled = data.aws_ami.latest.id           # references a data source — not evaluable
}

data "aws_ami" "latest" {
  most_recent = true
}

output "image" {
  description = "AMI image identifier consumers should plug into aws_instance.ami"
  value       = local.image
}

output "instance_count" {
  value     = var.instance_count
  sensitive = true
}
