resource "aws_eks_cluster" "primary" {
  name     = "primary"
  role_arn = "arn:aws:iam::111111111111:role/eks"

  kubernetes_network_config {
    ip_family         = "ipv4"
    service_ipv4_cidr = "10.200.0.0/16"
  }
}
