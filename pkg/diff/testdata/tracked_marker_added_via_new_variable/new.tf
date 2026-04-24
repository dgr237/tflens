variable "upgrade" {
  type    = bool
  default = true
}

locals {
  cluster_version = var.upgrade ? "1.35" : "1.34" # tflens:track: EKS minor — bump only after add-on compat
}
