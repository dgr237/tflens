module "vpc" {
  source = "terraform-aws-modules/vpc/aws"
  cidr   = 42
}
