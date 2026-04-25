resource "aws_vpc" "new" {}

moved {
  from = aws_vpc.old
  to   = aws_vpc.new
}
