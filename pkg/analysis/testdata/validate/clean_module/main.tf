variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
