variable "x" {
  type = string
  validation {
    condition     = length(var.x) > 5
    error_message = "b"
  }
}
