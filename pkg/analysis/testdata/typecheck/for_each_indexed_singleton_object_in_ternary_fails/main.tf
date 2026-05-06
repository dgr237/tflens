variable "queries" {
  type = map(object({
    metric_stat = optional(object({
      stat = string
    }))
  }))
}

resource "aws_X" "y" {
  for_each = var.queries["primary"].metric_stat != null ? var.queries["primary"].metric_stat : []
}
