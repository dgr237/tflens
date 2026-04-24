variable "upgrade" {
  type    = bool
  default = true
}

locals {
  cluster_version = var.upgrade ? "1.35" : "1.34"
}

module "eks" {
  source          = "./modules/eks"
  cluster_version = local.cluster_version
}
