terraform {
  required_version = ">= 1.3.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

# ---- variables ----

variable "env" {
  type        = string
  description = "Deployment environment"
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.env)
    error_message = "env must be dev, staging, or prod."
  }
}

variable "instance_count" {
  type    = number
  default = 1
}

variable "enable_monitoring" {
  type    = bool
  default = false
}

variable "tags" {
  type    = map(string)
  default = {}
}

# ---- locals ----

locals {
  name_prefix  = "${var.env}-app"
  is_prod      = var.env == "prod"
  min_count    = local.is_prod ? 2 : 1
  actual_count = var.instance_count > local.min_count ? var.instance_count : local.min_count
  common_tags  = merge(var.tags, { Environment = var.env, ManagedBy = "terraform" })
}

# ---- data sources ----

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

# ---- resources ----

resource "aws_vpc" "main" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = merge(local.common_tags, { Name = "${local.name_prefix}-vpc" })
}

resource "aws_subnet" "public" {
  count             = local.actual_count
  vpc_id            = aws_vpc.main.id
  cidr_block        = cidrsubnet("10.0.0.0/16", 8, count.index)
  availability_zone = data.aws_availability_zones.available.names[count.index]
  tags              = merge(local.common_tags, { Name = "${local.name_prefix}-public-${count.index}" })
}

resource "aws_security_group" "web" {
  name   = "${local.name_prefix}-web-sg"
  vpc_id = aws_vpc.main.id

  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.common_tags
}

resource "aws_instance" "web" {
  count         = local.actual_count
  ami           = data.aws_ami.ubuntu.id
  instance_type = local.is_prod ? "t3.medium" : "t3.micro"

  subnet_id              = aws_subnet.public[count.index].id
  vpc_security_group_ids = [aws_security_group.web.id]

  monitoring = var.enable_monitoring

  user_data = <<-EOF
    #!/bin/bash
    apt-get update -y
    apt-get install -y nginx
    systemctl enable nginx
    systemctl start nginx
  EOF

  tags = merge(local.common_tags, { Name = "${local.name_prefix}-web-${count.index}" })
}

# ---- for expressions ----

locals {
  instance_ids  = [for i in aws_instance.web : i.id]
  instance_map  = { for i in aws_instance.web : i.tags.Name => i.id }
  public_ips    = [for i in aws_instance.web : i.public_ip if i.public_ip != ""]
}

# ---- outputs ----

output "vpc_id" {
  value = aws_vpc.main.id
}

output "instance_ids" {
  value = local.instance_ids
}

output "web_urls" {
  value       = [for ip in local.public_ips : "http://${ip}"]
  description = "Public URLs for all web instances"
}
