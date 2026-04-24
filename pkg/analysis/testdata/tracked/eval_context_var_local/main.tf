variable "size" {
  type    = number
  default = 3
}

locals {
  doubled = var.size * 2
  label   = "size-${local.doubled}"
}
