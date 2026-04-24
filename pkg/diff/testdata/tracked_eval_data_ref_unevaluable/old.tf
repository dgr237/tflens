data "aws_caller_identity" "current" {}

locals {
  cluster_name = "${data.aws_caller_identity.current.account_id}-prod" # tflens:track: cluster name is force-new
}
