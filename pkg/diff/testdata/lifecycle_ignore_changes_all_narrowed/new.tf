resource "aws_vpc" "main" {
  lifecycle { ignore_changes = [tags] }
}
