variable "region" {
  type    = string
  default = "us-east-1"
}

module "child" {
  source = "./child"
  region = var.region
}
