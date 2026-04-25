locals {
  # values returns in key-sorted order, NOT insertion order.
  out = values({ "z" = "Z", "a" = "A", "m" = "M" })
}
