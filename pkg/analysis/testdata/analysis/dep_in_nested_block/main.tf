resource "aws_vpc" "main" {}
resource "aws_security_group" "web" {
  vpc_id = aws_vpc.main.id
  ingress {
    cidr_blocks = [aws_vpc.main.cidr_block]
  }
}
