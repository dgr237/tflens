# Small worked example for the export → kro RGD generator. Intentionally
# narrow — exercises the patterns that matter (variable refs, cross-resource
# refs, format(), nested blocks, dynamic blocks, an output) without sprawling.

variable "cluster_name" {
  type    = string
  default = "demo"
}

variable "k8s_version" {
  type    = string
  default = "1.31"
}

variable "ingress_rules" {
  type = list(object({
    from_port = number
    to_port   = number
    cidrs     = list(string)
  }))
  default = [
    { from_port = 443, to_port = 443, cidrs = ["10.0.0.0/8"] },
    { from_port = 22, to_port = 22, cidrs = ["10.1.0.0/16"] },
  ]
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

resource "aws_security_group" "cluster_sg" {
  name        = format("%s-cluster-sg", var.cluster_name)
  description = "EKS cluster ingress rules"

  # Dynamic block — generates one ingress block per element of
  # var.ingress_rules. Each iteration's content references the
  # iterator variable (defaults to the block label "ingress" since no
  # `iterator = X` is set) via ingress.value.<field>.
  dynamic "ingress" {
    for_each = var.ingress_rules
    content {
      from_port   = ingress.value.from_port
      to_port     = ingress.value.to_port
      protocol    = "tcp"
      cidr_blocks = ingress.value.cidrs
    }
  }

  # Static egress block alongside the dynamic ingress, demonstrating
  # that blocks and dynamic_blocks coexist on one resource.
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

output "cluster_endpoint" {
  value = aws_eks_cluster.this.endpoint
}
