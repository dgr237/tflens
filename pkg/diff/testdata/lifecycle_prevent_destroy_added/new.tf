resource "aws_vpc" "main" {
  lifecycle {
    prevent_destroy = true
  }
}
