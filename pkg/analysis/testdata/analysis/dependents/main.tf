resource "aws_vpc" "main" {}
resource "aws_subnet" "pub" { vpc_id = aws_vpc.main.id }
resource "aws_security_group" "web" { vpc_id = aws_vpc.main.id }
