module "net" {
  source = "./child"
  cidr   = "10.0.0.0/16"
  env    = "dev"
}
