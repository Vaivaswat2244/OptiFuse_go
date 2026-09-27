variable "subscription_id" {
  type = string
}

variable "resource_group_name" {
  type    = string
  default = "rg-optifuse-dev"
}

variable "cluster_name" {
  type    = string
  default = "aks-optifuse-dev"
}

variable "dns_zone" {
  description = "The delegated zone. The apex vaivaswat.me stays at Namecheap (Vercel site and email forwarding live there); only this subdomain is served by Azure DNS."
  type        = string
  default     = "optifuse.vaivaswat.me"
}
