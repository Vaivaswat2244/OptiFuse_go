output "resource_group_name" {
  value = azurerm_resource_group.this.name
}

output "cluster_name" {
  value = azurerm_kubernetes_cluster.this.name
}

output "oidc_issuer_url" {
  description = "Feed this to AWS IAM as an OIDC identity provider in Milestone 4."
  value       = azurerm_kubernetes_cluster.this.oidc_issuer_url
}

# The kubeconfig carries a client certificate that is cluster-admin. Marked
# sensitive so it never lands in plan output or CI logs; fetched with
# `terraform output -raw kube_config > ~/.kube/optifuse` when needed.
output "kube_config" {
  value     = azurerm_kubernetes_cluster.this.kube_config_raw
  sensitive = true
}
