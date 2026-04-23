variable "env" { type = string }
module "net" {
  source = "./child"
  env    = var.env
}
