locals {
  ids = concat(["a", "b"], ["c"]) # tflens:track: instance set — additions destroy state
}
