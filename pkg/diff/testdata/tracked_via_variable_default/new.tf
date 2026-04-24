variable "cluster_version" {
  type    = string
  default = "1.29"
}

resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = var.cluster_version # tflens:track: indirect via variable default
}
