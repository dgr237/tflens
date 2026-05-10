terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

resource "aws_eks_cluster" "this" {
  name     = "demo"
  role_arn = "arn:aws:iam::000000000000:role/r"

  vpc_config {
    subnet_ids = ["subnet-1", "subnet-2"]
  }

  kubernetes_network_config {
    service_ipv4_cidr = "10.100.0.0/16"
  }
}

# Nested-block + mid-path index: pre-fix, this returned nil because
# resolveSchemaPath rejected the [0] step after descending into the
# kubernetes_network_config block. With Fix 2 the index is consumed
# (nesting_mode = list) and the walk continues to service_ipv4_cidr.
output "service_cidr" {
  value = aws_eks_cluster.this.kubernetes_network_config[0].service_ipv4_cidr
}

# Bare nested-block reference (no index) — was already supported,
# included as a regression check.
output "vpc_block" {
  value = aws_eks_cluster.this.vpc_config
}

# Plain attribute reference — baseline.
output "cluster_name" {
  value = aws_eks_cluster.this.name
}
