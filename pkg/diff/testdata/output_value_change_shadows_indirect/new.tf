locals { a = "new" }
output "x" { value = local.a.id }
