# Small worked example for the export → kro RGD generator. Intentionally
# narrow — exercises the patterns that matter (variable refs, cross-resource
# refs, format(), nested blocks, an output) without sprawling.

variable "cluster_name" {
  type    = string
  default = "demo"
}

variable "k8s_version" {
  type    = string
  default = "1.31"
}

resource "aws_iam_role" "cluster" {
  name = format("%s-eks-role", var.cluster_name)

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "eks.amazonaws.com" }
    }]
  })
}

resource "aws_eks_cluster" "this" {
  name     = var.cluster_name
  version  = var.k8s_version
  role_arn = aws_iam_role.cluster.arn

  vpc_config {
    subnet_ids              = ["subnet-aaaa", "subnet-bbbb"]
    endpoint_private_access = true
    endpoint_public_access  = false
  }

  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = true
  }
}

output "cluster_endpoint" {
  value = aws_eks_cluster.this.endpoint
}
