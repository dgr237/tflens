variable "k" { type = map(string) }
resource "aws_iam_user" "u" { for_each = var.k }
