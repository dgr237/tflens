variable "config" {
  type = object({
    name = string
  })
}

module "net" {
  source   = "./child"
  capacity = var.config.name
}
