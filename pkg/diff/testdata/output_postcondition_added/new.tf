output "x" {
  value = "v"
  postcondition {
    condition     = self != ""
    error_message = "must be nonempty"
  }
}
