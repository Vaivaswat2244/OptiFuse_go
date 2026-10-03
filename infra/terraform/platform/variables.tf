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

variable "vercel_ip" {
  description = "Vercel's anycast A record for apex domains. Vercel shows the current value under Domains when a CNAME is not possible."
  type        = string
  default     = "216.198.79.1"
}
