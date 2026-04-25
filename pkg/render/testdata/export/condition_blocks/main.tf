variable "instance_count" {
  type    = number
  default = 3

  validation {
    condition     = var.instance_count > 0
    error_message = "instance_count must be positive"
  }

  validation {
    condition     = var.instance_count <= 100
    error_message = format("instance_count too high: got %d", var.instance_count)
  }
}

resource "aws_instance" "web" {
  count = var.instance_count

  lifecycle {
    precondition {
      condition     = var.instance_count <= 50
      error_message = "EKS limits — keep instance_count under 50"
    }
    postcondition {
      condition     = self.id != ""
      error_message = "instance must have an id after apply"
    }
  }
}

output "first_id" {
  value = aws_instance.web[0].id

  precondition {
    condition     = length(aws_instance.web) > 0
    error_message = "no instances were created"
  }
}
