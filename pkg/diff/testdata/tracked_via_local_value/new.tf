locals {
  cluster_version = "1.29"
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = local.cluster_version # tflens:track
}
