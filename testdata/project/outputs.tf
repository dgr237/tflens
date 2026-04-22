output "vpc_id" {
  value = module.network.vpc_id
}

output "instance_ids" {
  value = module.compute.instance_ids
}
