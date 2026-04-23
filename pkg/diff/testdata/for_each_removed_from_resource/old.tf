resource "aws_vpc" "main" { for_each = { a = 1 } }
