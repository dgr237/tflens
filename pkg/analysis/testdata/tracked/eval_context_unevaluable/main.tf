variable "ok" {
  type    = string
  default = "yes"
}

locals {
  derives = upper("hello")
}
