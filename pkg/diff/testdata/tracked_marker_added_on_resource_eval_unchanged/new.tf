variable "upgrade" {
  type    = bool
  default = false
}

locals {
  cluster_version = var.upgrade ? "1.35" : "1.34"
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = local.cluster_version # tflens:track: EKS minor — bump only after add-on compat
}
