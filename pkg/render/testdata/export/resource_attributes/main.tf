variable "env" {
  type    = string
  default = "prod"
}

resource "aws_instance" "web" {
  ami           = "ami-0c55b159cbfafe1f0"
  instance_type = var.size
  monitoring    = true

  tags = {
    Name = "web"
    Env  = var.env
    Tier = format("%s-frontend", var.env)
  }

  lifecycle {
    create_before_destroy = true
  }
}

data "aws_ami" "latest" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["ubuntu/*"]
  }
}
