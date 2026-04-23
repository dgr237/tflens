resource "aws_instance" "web" { ami = data.aws_ami.ghost.id }
