locals {
  tags = {
    Environment = var.env
    Region      = var.region
  }
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
  tags       = local.tags
}

resource "aws_subnet" "public" {
  count      = 2
  vpc_id     = aws_vpc.main.id
  cidr_block = cidrsubnet("10.0.0.0/16", 8, count.index)
  tags       = merge(local.tags, { Name = "public-${count.index}" })
}
