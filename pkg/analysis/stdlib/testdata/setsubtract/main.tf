locals {
  out = setsubtract(toset(["a", "b", "c"]), toset(["b"]))
}
