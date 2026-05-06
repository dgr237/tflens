variable "config" {
  type = object({
    label = string
  })
}

resource "aws_X" "y" {
  for_each = var.config.label != "" ? var.config.label : {}
}
