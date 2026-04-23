variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  for_each = var.tags
}
