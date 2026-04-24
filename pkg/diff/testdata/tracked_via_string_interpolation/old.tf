variable "env" {
  type    = string
  default = "prod"
}

locals {
  suffix = "primary"
}

resource "aws_eks_cluster" "this" {
  # cluster_name is force-new; changing it destroys and recreates the cluster
  cluster_name = "${var.env}-${local.suffix}" # tflens:track: force-new — destroys and recreates the cluster
}
