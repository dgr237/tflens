resource "aws_vpc" "main" {
  lifecycle {
    create_before_destroy = true
  }
}
