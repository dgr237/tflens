variable "region" {
  type = string
}

output "echo" {
  value = var.region
}
