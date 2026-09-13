data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

data "aws_ssm_parameter" "ubuntu" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

resource "aws_key_pair" "this" {
  key_name   = "${var.name}-key"
  public_key = file(pathexpand(var.ssh_public_key_path))
}

# 80 and 443 only. The executor holds the Privy authorization key and the
# keeper can move funds: both stay on the docker network, unreachable from here.
resource "aws_security_group" "this" {
  name        = "${var.name}-sg"
  description = "sweem: ssh, http, https"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "ssh"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.ssh_cidrs
  }

  ingress {
    description = "http, for the ACME challenge and the redirect to https"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "https"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "outbound: RPC, Privy, The Graph, package mirrors"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${var.name}-sg" }
}

resource "aws_instance" "this" {
  ami                         = data.aws_ssm_parameter.ubuntu.value
  instance_type               = var.instance_type
  subnet_id                   = data.aws_subnets.default.ids[0]
  vpc_security_group_ids      = [aws_security_group.this.id]
  key_name                    = aws_key_pair.this.key_name
  associate_public_ip_address = true

  root_block_device {
    volume_size = var.root_volume_gb
    volume_type = "gp3"
    encrypted   = true
  }

  tags = { Name = var.name }
}

# A static address so the DNS records survive a stop/start of the instance.
resource "aws_eip" "this" {
  instance = aws_instance.this.id
  domain   = "vpc"
  tags     = { Name = "${var.name}-eip" }
}

resource "local_file" "inventory" {
  filename        = "${path.module}/../ansible/inventory.ini"
  file_permission = "0644"

  content = <<-EOT
    [sweem]
    ${aws_eip.this.public_ip}

    [sweem:vars]
    ansible_user=ubuntu
    ansible_python_interpreter=/usr/bin/python3
    api_domain=${var.api_domain}
    market_domain=${var.market_domain}
    ui_origin=${var.ui_origin}
    acme_email=${var.acme_email}
  EOT
}
