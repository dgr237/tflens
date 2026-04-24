module "net" {
  source = "./child"
  cidr   = "10.0.0.0/16"
}
output "vpc_id" { value = module.net.vpc_id }
