locals {
  cluster_version = "1.28"
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = local.cluster_version # tflens:track
}
