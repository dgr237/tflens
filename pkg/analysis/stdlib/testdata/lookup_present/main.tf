locals {
  out = lookup({ "k" = "v" }, "k", "fallback")
}
