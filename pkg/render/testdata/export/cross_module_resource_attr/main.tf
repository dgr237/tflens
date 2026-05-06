terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

variable "name" {
  type    = string
  default = "demo"
}

# Cross-module schema-aware: parent's output threads through the
# child's output, which references aws_iam_role.this.arn. Pre-fix
# the child Module's value was resolved via schema-less
# InferExprType so this returned nil; post-fix the child Resolver
# carries the same provider schema and resolves cleanly.
module "child" {
  source = "./child"
  name   = var.name
}

output "child_role_arn" {
  value = module.child.role_arn
}

# Two-level cross-module — parent → child → grandchild. The
# recursive ChildModuleGetter feeds the grandchild's children to
# the descended resolver, so a chain like
# `output { value = module.grandchild.x }` inside the child still
# resolves through the grandchild's schema-aware analysis.
output "grandchild_arn" {
  value = module.child.grandchild_role_arn
}

# Baseline: variable passthrough — already worked pre-fix; included
# as a regression check to confirm the new code path doesn't break
# the schema-less route.
output "child_name" {
  value = module.child.name
}
