variable "size" {
  type    = number
  default = 42

  validation {
    condition     = var.size > 0
    error_message = "size must be positive"
  }
}

resource "aws_instance" "web" {
  ami = "ami-123"

  lifecycle {
    precondition {
      condition     = var.size <= 100
      error_message = "size capped"
    }
    postcondition {
      condition     = self.id != ""
      error_message = "id required"
    }
  }
}
