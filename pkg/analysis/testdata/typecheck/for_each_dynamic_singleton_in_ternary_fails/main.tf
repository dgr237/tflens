# Dynamic block whose for_each is the §7 ternary-fallback bug:
# `each.value.metric_stat` resolves through the resource for_each
# binding to a single object (`optional(object({...}))`), but the
# fallback is an empty list. Detection requires iterator-scope
# resolution — Option B's contribution.
variable "queries" {
  type = map(object({
    id          = string
    metric_stat = optional(object({ name = string }))
  }))
  default = {}
}

resource "aws_X" "y" {
  for_each = var.queries
  name     = each.value.id

  dynamic "metric_stat" {
    for_each = each.value.metric_stat != null ? each.value.metric_stat : []
    content {
      name = metric_stat.value.name
    }
  }
}
