variable "a" { type = map(string) }
variable "b" { type = map(string) }
resource "aws_instance" "w" {
  for_each = merge(var.a, var.b)
}
