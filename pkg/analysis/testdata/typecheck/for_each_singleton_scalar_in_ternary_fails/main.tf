variable "config" {
  type = object({
    name = string
  })
}

resource "aws_X" "y" {
  for_each = var.config.name != "" ? var.config.name : []
}
