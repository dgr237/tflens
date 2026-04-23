variable "k" { type = set(number) }
resource "aws_iam_user" "u" { for_each = var.k }
