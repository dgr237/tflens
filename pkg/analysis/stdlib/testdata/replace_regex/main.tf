locals {
  out = replace("abc123def", "/[0-9]+/", "N")
}
