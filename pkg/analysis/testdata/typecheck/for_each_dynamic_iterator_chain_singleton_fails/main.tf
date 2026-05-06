# Two-level iterator chain mirroring the upstream ECS pattern at
# main.tf:1712 / 1757. The outer dynamic binds `metric_data_query`
# to a list element (object); the inner dynamic's `for_each`
# is a bare reference to that element's `metric` field, which is
# itself a single object — the §7 sibling bug class. Requires the
# resolver to push the outer iterator's element type so the inner
# `metric_data_query.value.metric` resolves through it.
variable "policies" {
  type = list(object({
    metric_data_query = list(object({
      id     = string
      metric = object({ name = string })
    }))
  }))
  default = []
}

resource "aws_X" "y" {
  for_each = { for i, p in var.policies : tostring(i) => p }
  name     = each.key

  dynamic "metric_data_query" {
    for_each = each.value.metric_data_query
    content {
      id = metric_data_query.value.id

      dynamic "metric" {
        # BUG: metric_data_query.value.metric is a single object,
        # not a collection.
        for_each = metric_data_query.value.metric
        content {
          metric_name = metric.value.name
        }
      }
    }
  }
}
