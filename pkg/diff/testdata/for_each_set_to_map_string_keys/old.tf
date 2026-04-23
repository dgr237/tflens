variable "k" { type = set(string) }
resource "aws_iam_user" "u" { for_each = var.k }
