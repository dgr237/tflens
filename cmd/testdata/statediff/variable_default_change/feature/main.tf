variable "n" {
  type    = number
  default = 1
}

resource "aws_instance" "web" {
  count = var.n
}
