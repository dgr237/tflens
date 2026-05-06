variable "region" {
  type = string
}

variable "enabled_count" {
  type = number
}

variable "tags" {
  type    = map(string)
  default = {}
}

output "echo" {
  value = var.region
}
