variable "env" {
  type    = string
  default = "prod"
}

variable "scale" {
  type    = number
  default = 3
}

resource "aws_instance" "demo" {
  # Plain literal — ast collapses to a single literal_value node.
  ami = "ami-0c55b159cbfafe1f0"

  # Bare reference — scope_traversal with root + attr steps.
  instance_type = var.env

  # Function call with mixed literal + reference args — function_call.
  name = format("%s-instance-%d", var.env, var.scale)

  # Conditional — conditional with three sub-expressions.
  monitoring = var.env == "prod" ? true : false

  # Binary op — binary_op with two operand sub-expressions.
  ebs_optimized = var.scale > 2

  # Object literal with nested ref — object_cons containing scope_traversal.
  tags = {
    Name = format("%s-demo", var.env)
    Env  = var.env
  }

  # Splat — splat expression over a relative_traversal.
  security_group_ids = aws_security_group.sg[*].id
}

resource "aws_security_group" "sg" {
  count = 2

  # for-expression — for with collection + value_result.
  description = join(",", [for s in ["a", "b", "c"] : upper(s)])
}
