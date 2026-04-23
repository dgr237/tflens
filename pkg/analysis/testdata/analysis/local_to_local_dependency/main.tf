variable "env" {}
locals {
  is_prod = var.env == "prod"
  count   = local.is_prod ? 2 : 1
}
