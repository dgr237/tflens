locals {
  # Refactor: same string extracted from a tag via regex() rather
  # than typed as a literal. Effective value identical →
  # Informational, not Breaking.
  major = regex("^[0-9]+", "1.34.2") # tflens:track: major version channel
}
