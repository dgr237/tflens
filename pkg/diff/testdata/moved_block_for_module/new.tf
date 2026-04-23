module "network" { source = "./v1" }
moved {
  from = module.net
  to   = module.network
}
