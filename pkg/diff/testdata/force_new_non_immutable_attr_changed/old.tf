resource "aws_eks_cluster" "primary" {
  name     = "primary"
  role_arn = "arn:aws:iam::111111111111:role/eks"
  version  = "1.30"
}
