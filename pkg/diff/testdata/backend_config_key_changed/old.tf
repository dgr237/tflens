terraform {
  backend "s3" {
    bucket = "b"
    key    = "prod/terraform.tfstate"
  }
}
