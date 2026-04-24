resource "aws_eks_cluster" "this" {
  name = "prod"
  # tflens:track: own-line marker annotates the next attribute
  cluster_version = "1.28"
}
