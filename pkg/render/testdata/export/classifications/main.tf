# Fixture exercising the 0.8.0-prototype additions:
#   - count_kind: include_when (ternary 1:0 + 0:1) and scalar
#   - for_each_kind: list / map / object / invalid (ternary-fallback bug)
#   - validation folded: enum, min_length, max_length, length_range,
#     minimum, maximum, pattern, complex
#   - per-expression references with paths, indices, splats
#   - module-call argument child_variable_type binding via ./child

variable "enable_logs" {
  type    = bool
  default = false
}

variable "instances" {
  type = map(object({
    cpu      = number
    metric_stat = optional(object({ name = string }))
  }))
  default = {}
}

variable "regions" {
  type    = list(string)
  default = ["us-east-1"]

  validation {
    condition     = length(var.regions) > 0
    error_message = "at least one region required"
  }
}

variable "env" {
  type = string

  validation {
    condition     = contains(["dev", "staging", "prod"], var.env)
    error_message = "env must be dev, staging, or prod"
  }
}

variable "name" {
  type = string

  validation {
    condition     = length(var.name) >= 3 && length(var.name) <= 32
    error_message = "name must be 3-32 chars"
  }

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]*$", var.name))
    error_message = "name must be lowercase kebab-case"
  }
}

variable "fingerprint" {
  type = string

  # Fixed-length constraint — composegen §4.1 folds == to a degenerate
  # length_range with min == max.
  validation {
    condition     = length(var.fingerprint) == 64
    error_message = "fingerprint must be exactly 64 chars"
  }
}

variable "replicas" {
  type    = number
  default = 1

  validation {
    condition     = var.replicas >= 1
    error_message = "replicas must be at least 1"
  }

  validation {
    condition     = var.replicas <= 100
    error_message = "replicas must be at most 100"
  }
}

# count_kind: include_when (cond ? 1 : 0)
resource "aws_cloudwatch_log_group" "logs" {
  count = var.enable_logs ? 1 : 0
  name  = "demo"
}

# count_kind: include_when (cond ? 0 : 1 — flipped polarity)
resource "aws_iam_role" "default" {
  count = var.enable_logs ? 0 : 1
  name  = "fallback"
}

# count_kind: scalar
resource "aws_subnet" "static" {
  count = 3
  cidr  = "10.0.${count.index}.0/24"
}

# for_each_kind: map
resource "aws_instance" "web" {
  for_each      = var.instances
  instance_type = "t3.micro"
}

# for_each_kind: invalid — §7 ECS bug: object ternary with [] fallback.
# var.instances["primary"].metric_stat is a single object, not a collection.
resource "aws_cloudwatch_metric_alarm" "broken" {
  for_each = var.instances["primary"].metric_stat != null ? var.instances["primary"].metric_stat : []
  name     = "broken"
}

# Resource references with index and splat
output "first_subnet" {
  value = aws_subnet.static[0].cidr
}

output "all_subnets" {
  value = aws_subnet.static[*].cidr
}

module "child" {
  source        = "./child"
  region        = var.regions[0]
  enabled_count = var.replicas
  tags = {
    Name = "demo"
  }
}

# Variable typed for the nested-dynamic / iterator-scope case below:
# a list of objects each carrying a (singular) metric_stat field.
# The outer dynamic for_each's element type is the object; the inner
# dynamic's for_each then reaches `<iter>.value.metric_stat`, which is
# a single object (not a collection) — the §7 ECS bug shape.
variable "metric_queries" {
  type = list(object({
    id          = string
    metric_stat = optional(object({ name = string }))
  }))
  default = []
}

# Empty-object fallback (`x : {}`): for_each_kind should be "object"
# when X is a map; "invalid" when X is a single object.
variable "tag_overlay" {
  type    = map(string)
  default = {}
}

resource "aws_iam_policy" "tagged" {
  name = "demo"
  # for_each_kind: object (both branches resolve to map)
  for_each = var.enable_logs ? var.tag_overlay : {}
}

# Locals exercising the dependency graph + cycle marker. Chain:
#   pure_const  →  (no deps)
#   derived     →  pure_const + var.replicas
#   layered     →  derived (transitively pulls var.replicas)
#   cycle_a / cycle_b — deliberate cycle to exercise the marker
#
# Plus two false-cycle regressions that the previous walker
# incorrectly flagged:
#   diamond_root — refs both diamond_branch and pure_const directly,
#                  AND diamond_branch transitively refs pure_const
#                  (classical diamond — must NOT cycle).
#   multi_path   — refs the same target local twice with different
#                  paths (`local.shape.alpha` + `local.shape`).
#                  Both shape refs map to the same local; the walker
#                  must not flag the second occurrence as a cycle.
locals {
  pure_const     = "static"
  derived        = "${local.pure_const}-${var.replicas}"
  layered        = "wrap-${local.derived}"
  cycle_a        = local.cycle_b
  cycle_b        = local.cycle_a
  shape          = { alpha = "a", beta = "b" }
  diamond_branch = "${local.pure_const}-leaf"
  diamond_root   = "${local.diamond_branch}/${local.pure_const}"
  multi_path     = "${local.shape.alpha}-${local.shape}"
  # EKS-style: a local whose value is itself a ternary. Previously
  # the resolver couldn't see through this so `for_each_kind` against
  # `local.network_interfaces` fell back to "unknown". With the
  # ConditionalExpr handling, the ternary's empty-list fallback gets
  # peered through and the local's inferred_type populates as
  # list(string).
  network_interfaces = length(var.regions) > 0 ? var.regions : []
}

# Outputs exercising the new ConditionalExpr type-inference path.
# Both should now carry inferred_type — previously fell back to
# unknown and the field was omitted.
output "ternary_to_var" {
  value = var.enable_logs ? var.tag_overlay : {}
}

output "ternary_to_local" {
  value = length(var.regions) > 0 ? local.network_interfaces : []
}

# For-each on the inferred-type local. Should classify as "list"
# (vs the previous "unknown") because resolveExprType now resolves
# local.network_interfaces through its ternary value to list(string).
resource "aws_route" "via_local" {
  for_each = toset(local.network_interfaces)
  name     = each.key
}

# Output exercising inferred_type — value resolves through a variable
# whose declared type is map(string), so the output's inferred_type
# should surface that without re-running inference downstream.
output "tag_overlay_echo" {
  value = var.tag_overlay
}

# Output exercising splat with attr_path — `aws_subnet.static[*].cidr`
# projects each instance's `cidr` attribute, and the splat AST node
# should carry attr_path: ["cidr"].
output "splat_with_attr_path" {
  value = aws_subnet.static[*].cidr
}

# For-expression exercising the explicit binder_count + kind markers.
# Single-binder list form and two-binder object form.
output "single_binder_list" {
  value = [for x in var.regions : upper(x)]
}

output "two_binder_object" {
  value = { for k, v in var.tag_overlay : k => upper(v) }
}

# Conditional pattern markers — one per recognised pattern. Each
# output's value carries the conditional whose AST node should
# surface the named pattern.
output "pattern_drop_when_true" {
  value = var.enable_logs ? null : var.env
}

output "pattern_drop_when_false" {
  value = var.enable_logs ? var.env : null
}

output "pattern_null_check_lhs" {
  value = var.env != null ? upper(var.env) : "default"
}

resource "aws_cloudwatch_metric_alarm" "stack" {
  alarm_name = "stack"

  # Outer for_each: list of objects → kind: list
  dynamic "metric_data_query" {
    for_each = var.metric_queries
    content {
      id = metric_data_query.value.id

      # The §7 ECS bug nested inside dynamic content. The
      # iterator-scope resolver sees metric_data_query.value.metric_stat
      # as an OBJECT (single struct), not a collection — and the
      # fallback is empty list, so the kind should be "invalid"
      # with reason naming the single-value-iterated-as-list shape.
      dynamic "metric_stat" {
        for_each = metric_data_query.value.metric_stat != null ? metric_data_query.value.metric_stat : []
        content {
          metric_name = metric_stat.value.name
        }
      }
    }
  }
}
