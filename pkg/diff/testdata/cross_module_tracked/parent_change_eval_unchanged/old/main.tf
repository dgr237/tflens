locals {
  cluster_version = "1.34"
}

module "eks" {
  source          = "./modules/eks"
  cluster_version = local.cluster_version
}
