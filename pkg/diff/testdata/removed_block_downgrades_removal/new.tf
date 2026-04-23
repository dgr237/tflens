removed {
  from = aws_vpc.legacy
  lifecycle {
    destroy = false
  }
}
