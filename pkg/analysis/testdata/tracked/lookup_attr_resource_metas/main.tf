resource "aws_instance" "all_metas" {
  count      = 3
  provider   = aws.east
  depends_on = [aws_instance.dep]

  lifecycle {
    ignore_changes       = [tags]
    replace_triggered_by = [null_resource.bump.id]
  }
}

resource "aws_instance" "by_each" {
  for_each = toset(["a", "b"])
}
