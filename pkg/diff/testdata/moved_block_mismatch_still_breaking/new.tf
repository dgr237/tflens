resource "aws_vpc" "new_name" {}
moved {
  from = aws_vpc.different_name
  to   = aws_vpc.new_name
}
