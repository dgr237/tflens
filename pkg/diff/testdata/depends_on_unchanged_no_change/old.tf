resource "aws_vpc" "main" {
  depends_on = [aws_account.setup, aws_region.default]
}
