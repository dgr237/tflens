resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = "1.29" # tflens:track: bump only after add-on compatibility check
}
