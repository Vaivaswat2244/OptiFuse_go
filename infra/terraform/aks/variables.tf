variable "subscription_id" {
  description = "Azure subscription to deploy into. Not a secret, just an address."
  type        = string
}

variable "location" {
  description = "Azure region. Student subscriptions carry an Azure Policy that whitelists five regions; of those, Hyderabad is the nearest that offers B-series VMs without restriction."
  type        = string
  default     = "indiasouthcentral"
}

variable "environment" {
  description = "Short environment name used in resource names and tags."
  type        = string
  default     = "dev"
}

variable "node_vm_size" {
  description = "VM size for the single node pool. B2s is 2 vCPU / 4 GiB, the largest that fits a student subscription's 4-vCPU B-series quota with headroom for a second node."
  type        = string
  default     = "Standard_B2s"
}
