terraform {
  required_version = ">= 1.0"
  backend "s3" {
    bucket = "my-state"
    key    = "prod/terraform.tfstate"
  }
}
