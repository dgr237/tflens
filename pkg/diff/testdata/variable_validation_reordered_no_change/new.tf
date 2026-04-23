variable "x" {
  type = string
  validation {
    condition     = length(var.x) < 100
    error_message = "b"
  }
  validation {
    condition     = length(var.x) > 0
    error_message = "a"
  }
}
