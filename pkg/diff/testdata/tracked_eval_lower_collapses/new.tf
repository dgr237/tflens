locals {
  # Author lowercased a constant via lower() rather than typing it
  # in the canonical form. Effective string identical → Informational.
  region = lower("US-EAST-1") # tflens:track: target deployment region
}
