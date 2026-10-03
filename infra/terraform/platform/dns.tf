# A zone is the unit of authority. Creating it assigns four Azure nameservers;
# until Namecheap publishes NS records for "optifuse" pointing at them, nothing
# in this zone is reachable from the internet. That handoff is the one manual
# step, and it is one-time.
resource "azurerm_dns_zone" "optifuse" {
  name                = var.dns_zone
  resource_group_name = var.resource_group_name

  tags = local.tags
}

# The ingress controller's Service asks Azure for a load balancer. Reading the
# assigned IP back from the Service, rather than typing it in, is what makes the
# record follow the infrastructure: destroy and recreate the controller and the
# next apply fixes DNS with no human in the loop.
#
# depends_on defers this read to apply time on the first run, when the Service
# does not exist yet. Without it, plan would fail on "service not found".
data "kubernetes_service" "ingress" {
  metadata {
    name      = "ingress-nginx-controller"
    namespace = "ingress-nginx"
  }

  depends_on = [helm_release.ingress_nginx]
}

locals {
  ingress_ip = data.kubernetes_service.ingress.status[0].load_balancer[0].ingress[0].ip
}

# api.optifuse.vaivaswat.me: the gateway.
resource "azurerm_dns_a_record" "api" {
  name                = "api"
  zone_name           = azurerm_dns_zone.optifuse.name
  resource_group_name = var.resource_group_name
  ttl                 = 300
  records             = [local.ingress_ip]

  tags = local.tags
}

# *.optifuse.vaivaswat.me: everything else that will sit behind the same
# ingress (argocd, grafana). Ingress routes by Host header, so one IP serves
# any number of names.
resource "azurerm_dns_a_record" "wildcard" {
  name                = "*"
  zone_name           = azurerm_dns_zone.optifuse.name
  resource_group_name = var.resource_group_name
  ttl                 = 300
  records             = [local.ingress_ip]

  tags = local.tags
}

# optifuse.vaivaswat.me itself: the frontend, on Vercel.
#
# Vercel suggests a CNAME, but this name is the apex of the delegated zone and
# an apex cannot be a CNAME (it already carries SOA and NS records). An A record
# to Vercel's anycast address is how Vercel handles root domains, and it is the
# same address the portfolio apex already uses.
resource "azurerm_dns_a_record" "frontend" {
  name                = "@"
  zone_name           = azurerm_dns_zone.optifuse.name
  resource_group_name = var.resource_group_name
  ttl                 = 300
  records             = [var.vercel_ip]

  tags = local.tags
}
