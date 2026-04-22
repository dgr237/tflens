output "instance_ids" {
  value = [for i in aws_instance.web : i.id]
}
