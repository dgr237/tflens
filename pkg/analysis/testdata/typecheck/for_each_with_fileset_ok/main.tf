resource "aws_s3_object" "files" {
  for_each = fileset("./", "*.txt")
}
