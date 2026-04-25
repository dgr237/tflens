variable "env" {}

module "remote" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"
}
