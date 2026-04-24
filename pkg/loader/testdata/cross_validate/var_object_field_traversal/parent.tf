variable "config" {
  type = object({
    property = number
  })
}

module "net" {
  source   = "./child"
  property = var.config.property
}
