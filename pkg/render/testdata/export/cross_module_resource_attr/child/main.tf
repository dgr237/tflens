variable "name" { type = string }

resource "aws_iam_role" "this" {
  name               = var.name
  assume_role_policy = "{}"
}

module "grandchild" {
  source = "./grandchild"
  name   = "${var.name}-gc"
}

# Resource attribute reference — the schema-aware resolution path.
output "role_arn" {
  value = aws_iam_role.this.arn
}

# Two-level cross-module passthrough — the recursion path.
output "grandchild_role_arn" {
  value = module.grandchild.role_arn
}

# Variable passthrough — pre-existing behaviour.
output "name" {
  value = var.name
}
