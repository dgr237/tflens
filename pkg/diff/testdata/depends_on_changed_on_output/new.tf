output "x" {
  value      = "v"
  depends_on = [aws_vpc.new]
}
