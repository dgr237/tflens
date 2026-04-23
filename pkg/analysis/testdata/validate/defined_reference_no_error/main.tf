variable "env" {}
data "aws_ami" "ubuntu" { most_recent = true }
locals { prefix = var.env }
resource "aws_instance" "web" {
  ami  = data.aws_ami.ubuntu.id
  tags = { Name = local.prefix }
}
