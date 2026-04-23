resource "aws_iam_user" "u" { for_each = toset(var.names) }
