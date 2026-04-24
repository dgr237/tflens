resource "aws_eks_cluster" "this" {
  name = "prod"

  # whitespace + comment-only changes; the value is unchanged
  cluster_version = "1.28" # tflens:track
}
