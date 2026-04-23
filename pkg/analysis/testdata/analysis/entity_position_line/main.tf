
variable "a" {}
variable "b" {}
locals {
  x = 1
  y = 2
}
resource "aws_vpc" "main" {}
output "id" { value = aws_vpc.main.id }
