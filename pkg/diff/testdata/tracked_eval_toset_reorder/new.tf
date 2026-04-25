locals {
  # Same effective set as old — toset folds the dupe and discards
  # order. Should be Informational, not Breaking.
  ids = toset(["c", "a", "b", "a"]) # tflens:track: source-of-truth instance set
}
