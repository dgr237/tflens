variable "config" {
  type = object({
    metric = optional(object({
      name = string
    }))
  })
}

resource "aws_X" "y" {
  for_each = var.config.metric != null ? var.config.metric : []
}
