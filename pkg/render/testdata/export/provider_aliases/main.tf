terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
      configuration_aliases = [aws.east, aws.west]
    }
  }
}

# Default provider instance — no alias.
provider "aws" {
  region = "us-east-1"
}

# East-coast non-default — referenced via aws.east.
provider "aws" {
  alias  = "east"
  region = "us-east-1"
}

# West-coast non-default with a profile attribute, to exercise multi-
# attribute Config capture.
provider "aws" {
  alias                       = "west"
  region                      = "us-west-2"
  profile                     = "shared-services"
  default_tags {
    tags = {
      Environment = "prod"
    }
  }
}

resource "aws_vpc" "default_region" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_vpc" "east_region" {
  provider   = aws.east
  cidr_block = "10.1.0.0/16"
}
