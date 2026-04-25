moved {
  from = data.aws_ami.old
  to   = data.aws_ami.new
}

moved {
  from = module.old_call
  to   = module.new_call
}
