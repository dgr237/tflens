data "aws_caller_identity" "current" {}

locals {
  cluster_name = "${data.aws_caller_identity.current.account_id}-staging" # tflens:track: cluster name is force-new
}
