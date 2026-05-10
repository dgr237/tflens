# Same shape as the buggy case but with the correct singleton-wrap:
# `[each.value.metric_stat]` — Terraform iterates a one-element list
# instead of misinterpreting the object's attributes. Detection must
# NOT flag this.
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
    for_each = each.value.metric_stat != null ? [each.value.metric_stat] : []
    content {
      name = metric_stat.value.name
    }
  }
}
