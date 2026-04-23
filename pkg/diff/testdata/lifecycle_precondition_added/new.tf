resource "aws_vpc" "main" {
  lifecycle {
    precondition {
      condition     = var.env != ""
      error_message = "env required"
    }
  }
}
