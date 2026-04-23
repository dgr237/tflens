variable "env" {}
output "name" { value = [for s in ["a", "b"] : s] }
