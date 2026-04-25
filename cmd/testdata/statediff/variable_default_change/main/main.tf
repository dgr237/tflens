variable "n" {
  type    = number
  default = 3
}

resource "aws_instance" "web" {
  count = var.n
}
