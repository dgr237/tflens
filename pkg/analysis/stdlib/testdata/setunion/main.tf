locals {
  out = setunion(toset(["a", "b"]), toset(["b", "c"]))
}
