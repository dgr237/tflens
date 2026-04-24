resource "aws_db_instance" "main" {
  engine_version = "15.4" # tflens:track: requires planned maintenance window
}
