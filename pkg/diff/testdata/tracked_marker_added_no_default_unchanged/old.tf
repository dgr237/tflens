variable "cluster_version" {
  type = string
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = var.cluster_version
}
