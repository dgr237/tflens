variable "names" { type = list(string) }
resource "aws_iam_user" "u" {
  for_each = var.names
}
