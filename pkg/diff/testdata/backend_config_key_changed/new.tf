terraform {
  backend "s3" {
    bucket = "b"
    key    = "staging/terraform.tfstate"
  }
}
