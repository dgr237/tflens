locals {
  # Refactor: same string assembled via format() instead of inlined.
  # Effective value unchanged → Informational, not Breaking.
  image = format("ec2-%s-v%d", "small", 3) # tflens:track: AMI image identifier
}
