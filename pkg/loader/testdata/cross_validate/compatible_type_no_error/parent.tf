module "net" {
  source    = "./child"
  instances = 3
  name      = "app"
}
