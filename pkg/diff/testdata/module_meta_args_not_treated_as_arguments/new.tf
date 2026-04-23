module "net" {
  source     = "./net"
  count      = 3
  depends_on = [aws_vpc.main]
}
