data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
}

resource "aws_instance" "web" {
  count         = length(var.subnet_ids)
  ami           = data.aws_ami.ubuntu.id
  instance_type = var.env == "prod" ? "t3.medium" : "t3.micro"
  subnet_id     = var.subnet_ids[count.index]

  tags = {
    Name        = "${var.env}-web-${count.index}"
    Environment = var.env
  }
}
