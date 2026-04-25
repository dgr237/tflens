locals {
  # Same string assembled via format() — text differs but the
  # evaluated value matches. SensitiveChange should not fire.
  image = format("ec2-%s-v%d", "small", 3)
}

resource "aws_instance" "web" {
  for_each = toset([local.image])
}
