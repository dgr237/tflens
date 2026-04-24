variable "env" {
  type    = string
  default = "prod"
}

locals {
  versions_by_env = {
    prod    = "1.28"
    staging = "1.30"
  }
  cluster_version = local.versions_by_env[var.env]
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = local.cluster_version # tflens:track
}
