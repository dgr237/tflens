variable "password" {
  type      = string
  sensitive = true
}
output "pw" { value = var.password }
