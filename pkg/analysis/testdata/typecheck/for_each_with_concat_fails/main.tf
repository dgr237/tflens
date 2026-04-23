resource "aws_instance" "w" {
  for_each = concat(["a"], ["b"])
}
