locals {
  out = setintersection(toset(["a", "b", "c"]), toset(["b", "c", "d"]))
}
