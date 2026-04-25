module "vpc" {
  source  = "ns/vpc/aws"
  version = "1.0.0"
}

module "rds" {
  source  = "ns/rds/aws"
  version = "1.0.0"
}
