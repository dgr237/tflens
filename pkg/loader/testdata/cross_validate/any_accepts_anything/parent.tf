module "net" {
  source = "./child"
  cfg    = { a = 1, b = 2 }
}
