variable "config" {
  type = object({
    network = object({
      cidr = string
    })
  })
}

module "net" {
  source = "./child"
  cidr   = var.config.network.cidr
}
