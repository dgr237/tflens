terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source = "hashicorp/random"
    }
  }

  backend "s3" {
    bucket = "tfstate"
    key    = "envs/prod/terraform.tfstate"
    region = "us-east-1"
  }
}

variable "env" {
  type    = string
  default = "prod"
}

module "vpc" {
  source  = "registry.example.com/ns/vpc/aws"
  version = "1.2.3"
}

module "local_kid" {
  source = "./child"
}

resource "aws_instance" "web" {
  ami        = "ami-123"
  subnet_id  = module.vpc.public_subnet_id
  depends_on = [module.vpc]

  lifecycle {
    ignore_changes = [tags]
  }
}

moved {
  from = aws_instance.old_web
  to   = aws_instance.web
}

removed {
  from = aws_instance.legacy
}
