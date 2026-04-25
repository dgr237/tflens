locals {
  out = coalescelist([], ["first-non-empty"], ["later"])
}
