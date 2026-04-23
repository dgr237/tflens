variable "env" { type = string }
module "net" {
  source    = "./child"
  instances = var.env
}
