# A resource group is the unit of lifecycle in Azure: every resource lives in
# exactly one, and deleting the group deletes everything inside it. Terraform
# tracks each resource individually, but the group is the blast radius.
resource "azurerm_resource_group" "this" {
  name     = "rg-optifuse-${var.environment}"
  location = var.location

  tags = local.tags
}

resource "azurerm_kubernetes_cluster" "this" {
  name                = "aks-optifuse-${var.environment}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  dns_prefix          = "optifuse-${var.environment}"

  # Free tier: no charge for the control plane, no uptime SLA. The API server
  # can be briefly unavailable during upgrades. Fine for one node; a paid tier
  # is the first thing to change if this ever carries real traffic.
  sku_tier = "Free"

  # The control plane publishes an OIDC discovery document, which lets a pod's
  # service-account token be verified by anyone who trusts that issuer. AWS IAM
  # can. That is how the enricher will call CloudWatch with no stored AWS key.
  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  # Azure CNI in overlay mode: pods get addresses from a private overlay range
  # rather than consuming subnet IPs, so a small VNet never runs out. The
  # standard load balancer is what turns a `type: LoadBalancer` Service into a
  # public IP.
  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    load_balancer_sku   = "standard"
  }

  default_node_pool {
    name       = "system"
    vm_size    = var.node_vm_size
    node_count = 1

    # B2s ships a 128 GiB OS disk by default, billed monthly. 32 GiB holds the
    # OS plus a handful of images comfortably and costs a quarter as much.
    os_disk_size_gb = 32

    # Without this block Terraform proposes a change on every plan, because
    # AKS fills the defaults in server-side.
    upgrade_settings {
      max_surge = "10%"
    }

    tags = local.tags
  }

  # The cluster itself needs an identity to create load balancers, disks and
  # public IPs in Azure. System-assigned means Azure creates and rotates it;
  # nothing to store.
  identity {
    type = "SystemAssigned"
  }

  tags = local.tags
}

locals {
  tags = {
    project     = "optifuse"
    environment = var.environment
    managed_by  = "terraform"
  }
}
