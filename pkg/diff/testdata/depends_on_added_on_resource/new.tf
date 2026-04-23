resource "aws_vpc" "main" {
  depends_on = [aws_account.setup]
}
