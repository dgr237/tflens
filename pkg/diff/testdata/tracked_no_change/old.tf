resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = "1.28" # tflens:track
}
