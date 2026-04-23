resource "aws_iam_user" "u" {
  for_each = "not-a-set"
}
