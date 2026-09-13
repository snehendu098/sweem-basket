variable "region" {
  type        = string
  default     = "us-east-1"
  description = "AWS region for the instance."
}

variable "name" {
  type        = string
  default     = "sweem"
  description = "Prefix for every resource name."
}

# The Rust executor is the reason this is not a t3.micro: a release build of
# alloy needs more than 2 GB. The playbook also adds swap.
variable "instance_type" {
  type        = string
  default     = "t3.medium"
  description = "EC2 instance type. t3.small works only if you build images elsewhere."
}

variable "root_volume_gb" {
  type        = number
  default     = 30
  description = "Root EBS size. Docker images and build cache live here."
}

variable "ssh_public_key_path" {
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
  description = "Public key installed on the instance for the ubuntu user."
}

# Default is open because a laptop IP changes and a locked-out box is worse than
# a key-only SSH port. Narrow it to your own address if the box is long-lived.
variable "ssh_cidrs" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = "CIDRs allowed to reach port 22."
}

variable "api_domain" {
  type        = string
  default     = "api.basket.sweem.org"
  description = "Hostname for the wallet API. Caddy gets a certificate for it."
}

variable "market_domain" {
  type        = string
  default     = "market.basket.sweem.org"
  description = "Hostname for the market-data API."
}

variable "ui_origin" {
  type        = string
  default     = "https://basket.sweem.org"
  description = "Browser origin allowed by CORS on both APIs."
}

variable "acme_email" {
  type        = string
  description = "Email Let's Encrypt uses for expiry notices."
}
