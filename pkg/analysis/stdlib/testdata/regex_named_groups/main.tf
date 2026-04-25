locals {
  out = regex("(?P<word>[a-z]+)-(?P<num>[0-9]+)", "abc-123-def")
}
