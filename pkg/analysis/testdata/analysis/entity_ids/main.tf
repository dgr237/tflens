variable "env" {}
data "aws_ami" "ubuntu" {}
resource "aws_instance" "web" {}
output "id" { value = aws_instance.web.id }
