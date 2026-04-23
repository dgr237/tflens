variable "n" { type = number }
resource "aws_instance" "w" { count = var.n }
