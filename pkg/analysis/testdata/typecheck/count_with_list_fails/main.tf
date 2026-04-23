resource "aws_instance" "w" { count = ["a", "b"] }
