variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  count = keys(var.tags)
}
