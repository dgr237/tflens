resource "aws_vpc" "main" {}
module "net" {
  source = "./child"
  cidr   = aws_vpc.main.cidr_block
}
