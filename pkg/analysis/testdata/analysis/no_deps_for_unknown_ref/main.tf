resource "aws_subnet" "pub" {
  count      = 3
  cidr_block = cidrsubnet("10.0.0.0/16", 8, count.index)
}
