locals {
  # Refactor: same policy expressed as an HCL object + jsonencode()
  # rather than a raw string. Effective JSON identical (both keys
  # serialise in sorted order) → Informational, not Breaking.
  policy = jsonencode({ Action = "s3:GetObject", Effect = "Allow" }) # tflens:track: IAM policy document
}
