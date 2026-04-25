locals {
  out = setsymmetricdifference(toset(["a", "b"]), toset(["b", "c"]))
}
