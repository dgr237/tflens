resource "aws_instance" "w" {
  for_each = some_function(arg)
}
