module "envs" {
  source   = "./envs"
  for_each = [1, 2, 3]
}
