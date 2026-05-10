variable "config" {
  type = object({
    items = optional(list(string))
  })
}

resource "aws_X" "y" {
  for_each = var.config.items != null ? toset(var.config.items) : []
}
