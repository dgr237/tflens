resource "aws_subnet" "pub" {
  for_each = ["a", "b", "c"]
}
