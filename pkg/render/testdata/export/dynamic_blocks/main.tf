variable "ingress_rules" {
  type = list(object({
    from_port = number
    to_port   = number
    cidrs     = list(string)
  }))
  default = [
    { from_port = 443, to_port = 443, cidrs = ["10.0.0.0/8"] },
    { from_port = 22, to_port = 22, cidrs = ["10.1.0.0/16"] },
  ]
}

resource "aws_security_group" "sg" {
  name = "demo"

  # Default iterator (block label "ingress")
  dynamic "ingress" {
    for_each = var.ingress_rules
    content {
      from_port   = ingress.value.from_port
      to_port     = ingress.value.to_port
      protocol    = "tcp"
      cidr_blocks = ingress.value.cidrs
    }
  }

  # Explicit iterator (renamed to `rule`) — proves the iterator field
  # populates when set, and that the content body's references through
  # the renamed iterator are captured correctly.
  dynamic "egress" {
    for_each = var.ingress_rules
    iterator = rule
    content {
      from_port   = rule.value.from_port
      to_port     = rule.value.to_port
      protocol    = "tcp"
      cidr_blocks = rule.value.cidrs
    }
  }

  # A static block alongside the dynamic ones — proves blocks vs
  # dynamic_blocks coexist on the same resource.
  tags {
    Name = "demo"
  }
}
