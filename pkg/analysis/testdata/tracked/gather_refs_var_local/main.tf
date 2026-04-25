variable "name" {
  type    = string
  default = "prod"
}

locals {
  region = "us-east-1"
}

# Output exists solely so the test can grab a real expression to feed
# into GatherRefsFromExpr.
output "summary" {
  value = "${var.name}-${local.region}"
}
