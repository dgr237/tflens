locals {
  # Restructured via merge() but the resulting object's key→value
  # pairs are identical. Should be Informational, not Breaking.
  tags = merge({ "env" = "prod" }, { "team" = "platform" }) # tflens:track: identity tags
}
