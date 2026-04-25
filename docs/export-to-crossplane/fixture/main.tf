# Crossplane converter fixture. Same shape as the kro POC fixture
# minus the `dynamic "ingress"` block — classic Compositions don't
# have a clean way to iterate (that's what Composition Functions are
# for) so dynamic blocks are documented as out-of-scope for this POC.
#
# Patterns demonstrated end-to-end:
#   - Terraform variables → XRD spec.parameters
#   - Variable refs inside attributes → patches with fromFieldPath
#   - format() on a var → patches + string-transform fmt
#   - Cross-resource refs (aws_iam_role.cluster.arn) → emitted as a
#     TODO comment + a placeholder selector (Crossplane has several
#     options: cross-resource references via name, MatchControllerRef,
#     external-name annotations; converter author picks per scenario)
#   - Nested blocks (vpc_config, access_config) → spec.forProvider.X
#     with the same camelCase rename Upbound's provider-aws uses

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
