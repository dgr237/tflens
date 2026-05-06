# Trimmed mirror of terraform-aws-modules/terraform-aws-ecs v6.9.0
# modules/service — exercises the full iterator chain that surfaces
# the §7 bug class. Captures the same shape as lines 1518 → 1759 of
# the upstream module, focused on the resource for_each → 5 levels
# of nested dynamic blocks → metric_stat for_each.
#
# Three for_each sites at the deepest level differ in shape:
#
#   - "metric_stat_buggy_capacity"    — `cond ? singleton : []`  (BUG)
#   - "metric_stat_buggy_load"        — `cond ? singleton : []`  (BUG)
#   - "metric_stat_correct_scaling"   — `cond ? [singleton] : []` (FIX)
#
# Expectation: the buggy two should classify as
# {kind: "invalid", expected: "list"}; the correct one as {kind: "list"}.

variable "autoscaling_policies" {
  type = map(object({
    policy_type = optional(string, "TargetTrackingScaling")
    predictive_scaling_policy_configuration = optional(object({
      metric_specification = list(object({
        customized_capacity_metric_specification = optional(object({
          metric_data_query = list(object({
            id = string
            metric_stat = optional(object({
              metric = object({
                metric_name = string
              })
              stat = string
            }))
          }))
        }))
        customized_load_metric_specification = optional(object({
          metric_data_query = list(object({
            id = string
            metric_stat = optional(object({
              metric = object({
                metric_name = string
              })
              stat = string
            }))
          }))
        }))
        customized_scaling_metric_specification = optional(object({
          metric_data_query = list(object({
            id = string
            metric_stat = optional(object({
              metric = object({
                metric_name = string
              })
              stat = string
            }))
          }))
        }))
        target_value = number
      }))
    }))
  }))
  default = {}
}

variable "enable_autoscaling" {
  type    = bool
  default = true
}

resource "aws_appautoscaling_policy" "this" {
  for_each = { for k, v in var.autoscaling_policies : k => v if var.enable_autoscaling }

  name        = each.key
  policy_type = each.value.policy_type

  dynamic "predictive_scaling_policy_configuration" {
    for_each = each.value.policy_type == "PredictiveScaling" && each.value.predictive_scaling_policy_configuration != null ? [each.value.predictive_scaling_policy_configuration] : []

    content {
      dynamic "metric_specification" {
        for_each = predictive_scaling_policy_configuration.value.metric_specification

        content {
          dynamic "customized_capacity_metric_specification" {
            for_each = metric_specification.value.customized_capacity_metric_specification != null ? [metric_specification.value.customized_capacity_metric_specification] : []

            content {
              dynamic "metric_data_query" {
                for_each = customized_capacity_metric_specification.value.metric_data_query

                content {
                  id = metric_data_query.value.id

                  # BUG (mirrors upstream main.tf:1669):
                  # cond ? singleton : []  → kind: "invalid"
                  dynamic "metric_stat_buggy_capacity" {
                    for_each = metric_data_query.value.metric_stat != null ? metric_data_query.value.metric_stat : []
                    content {
                      stat = metric_stat_buggy_capacity.value.stat

                      # SIBLING BUG (mirrors upstream main.tf:1712):
                      # bare singleton object as for_each (no ternary)
                      # — `.metric` is `object(...)` per the type
                      # declaration, not a list. Detected without
                      # ternary anchor via the single-value rule.
                      dynamic "metric_inner_buggy_capacity" {
                        for_each = metric_stat_buggy_capacity.value.metric
                        content {
                          metric_name = metric_inner_buggy_capacity.value.metric_name
                        }
                      }
                    }
                  }
                }
              }
            }
          }

          dynamic "customized_load_metric_specification" {
            for_each = metric_specification.value.customized_load_metric_specification != null ? [metric_specification.value.customized_load_metric_specification] : []

            content {
              dynamic "metric_data_query" {
                for_each = customized_load_metric_specification.value.metric_data_query

                content {
                  id = metric_data_query.value.id

                  # BUG (mirrors upstream main.tf:1714):
                  # cond ? singleton : []  → kind: "invalid"
                  dynamic "metric_stat_buggy_load" {
                    for_each = metric_data_query.value.metric_stat != null ? metric_data_query.value.metric_stat : []
                    content {
                      stat = metric_stat_buggy_load.value.stat
                    }
                  }
                }
              }
            }
          }

          dynamic "customized_scaling_metric_specification" {
            for_each = metric_specification.value.customized_scaling_metric_specification != null ? [metric_specification.value.customized_scaling_metric_specification] : []

            content {
              dynamic "metric_data_query" {
                for_each = customized_scaling_metric_specification.value.metric_data_query

                content {
                  id = metric_data_query.value.id

                  # FIX (mirrors upstream main.tf:1759):
                  # cond ? [singleton] : []  → kind: "list"
                  dynamic "metric_stat_correct_scaling" {
                    for_each = metric_data_query.value.metric_stat != null ? [metric_data_query.value.metric_stat] : []
                    content {
                      stat = metric_stat_correct_scaling.value.stat
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}
