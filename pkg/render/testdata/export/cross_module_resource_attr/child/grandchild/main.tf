variable "name" { type = string }

resource "aws_iam_role" "this" {
  name               = var.name
  assume_role_policy = "{}"
}

output "role_arn" {
  value = aws_iam_role.this.arn
}
