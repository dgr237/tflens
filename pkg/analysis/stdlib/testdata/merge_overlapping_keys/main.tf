locals {
  # Later argument wins on overlap — Terraform-spec behaviour.
  out = merge({ "k" = "first" }, { "k" = "last" })
}
