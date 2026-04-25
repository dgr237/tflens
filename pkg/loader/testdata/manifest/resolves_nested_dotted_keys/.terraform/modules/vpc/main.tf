variable "cidr" {
  type = string
}

module "sg" {
  source = "./submodules/sg"
  name   = "default"
}
