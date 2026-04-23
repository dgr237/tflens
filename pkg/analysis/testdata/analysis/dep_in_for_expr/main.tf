resource "aws_instance" "web" {}
locals { ids = [for i in aws_instance.web : i.id] }
