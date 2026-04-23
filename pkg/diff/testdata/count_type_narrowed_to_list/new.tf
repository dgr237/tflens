variable "n" { type = list(string) }
resource "aws_instance" "w" { count = var.n }
