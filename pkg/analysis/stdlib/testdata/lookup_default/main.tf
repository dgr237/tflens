locals {
  out = lookup({ "k" = "v" }, "missing", "fallback")
}
