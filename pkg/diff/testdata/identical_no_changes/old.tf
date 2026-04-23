variable "env" {
  type    = string
  default = "dev"
}
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
output "id" { value = aws_vpc.main.id }
