locals {
  # An element was actually added — concat result has 4 elements
  # instead of 3. Must still be Breaking despite both sides going
  # through concat(). Pin so the new value-equivalent short-circuit
  # doesn't accidentally over-suppress real changes.
  ids = concat(["a", "b"], ["c", "d"]) # tflens:track: instance set — additions destroy state
}
