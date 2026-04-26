// A small VPC + subnet shape used by the dogfood workflow at
// .github/workflows/dogfood.yml to exercise the GitHub Action wrapper
// end-to-end on every PR. Doubles as a public reference example for
// the # tflens:track marker, count-driven resources, and the static-
// evaluation surface (merge() of locals).
//
// There's no `terraform { required_providers { ... } }` block — tflens
// is schema-free, so the analysis runs without an AWS provider
// download. To `terraform plan` this for real, add a provider block.

variable "vpc_cidr" {
  type    = string
  default = "10.1.0.0/16" // [demo] tracked attribute change — diff should flag Breaking via the # tflens:track marker on aws_vpc.main.cidr_block
}

variable "subnet_count" {
  type    = number
  default = 1 // [demo] 3 → 1, statediff should flag Breaking via the count-reaches-changed-default detector
}

variable "tags" {
  type = map(string)
  // [demo] default removed → diff should flag Breaking ("variable now required")
}

locals {
  common_tags = merge(var.tags, {
    Module = "tflens-dogfood-example"
  })
}

resource "aws_vpc" "main" {
  // tflens:track: VPC CIDR is load-bearing — coordinate with downstream consumers
  cidr_block = var.vpc_cidr
  tags       = local.common_tags
}

resource "aws_subnet" "public" {
  count             = var.subnet_count
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.${count.index}.0/24"
  availability_zone = "us-east-1${["a", "b", "c", "d", "e"][count.index]}"
  tags              = local.common_tags
}

output "vpc_id" {
  value = aws_vpc.main.id
}

output "subnet_ids" {
  value = aws_subnet.public[*].id
}
