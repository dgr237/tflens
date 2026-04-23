module "net" {
  source = "./net"
  cidr   = "10.0.0.0/16"
  region = "us-east-1"
}
