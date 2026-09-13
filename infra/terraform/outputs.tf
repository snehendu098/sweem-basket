output "public_ip" {
  value       = aws_eip.this.public_ip
  description = "Elastic IP. Both A records point here."
}

output "ssh" {
  value       = "ssh ubuntu@${aws_eip.this.public_ip}"
  description = "Shell on the box."
}

output "dns_records" {
  description = "Create these at your DNS provider before running the playbook: Caddy cannot get a certificate until they resolve."
  value       = <<-EOT
    A     ${var.api_domain}     ${aws_eip.this.public_ip}
    A     ${var.market_domain}  ${aws_eip.this.public_ip}
  EOT
}
