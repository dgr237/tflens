variable "x" {
  type = string
  precondition {
    condition     = length(var.x) > 0
    error_message = "must be nonempty"
  }
}
