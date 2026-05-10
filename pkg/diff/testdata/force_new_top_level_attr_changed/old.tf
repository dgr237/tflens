resource "aws_eks_cluster" "primary" {
  name     = "old-cluster"
  role_arn = "arn:aws:iam::111111111111:role/eks-old"
}
