variable "x" {
  type = string
  validation {
    condition     = length(var.x) > 0
    error_message = "must not be empty"
  }
}
