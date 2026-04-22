locals {
  name_prefix = "${var.env}-${var.region}"
}

module "network" {
  source = "./modules/network"

  env    = var.env
  region = var.region
}

module "compute" {
  source = "./modules/compute"

  env        = var.env
  vpc_id     = module.network.vpc_id
  subnet_ids = module.network.subnet_ids
}

