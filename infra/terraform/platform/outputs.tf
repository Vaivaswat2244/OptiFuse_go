output "name_servers" {
  description = "Publish these as NS records for host 'optifuse' at Namecheap."
  value       = azurerm_dns_zone.optifuse.name_servers
}

output "ingress_ip" {
  value = local.ingress_ip
}
