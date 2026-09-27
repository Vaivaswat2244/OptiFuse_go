terraform {
  required_version = ">= 1.9"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  subscription_id = var.subscription_id

  # Terraform normally registers every Azure resource provider it might need on
  # first use. That is slow and hides a real step, so the six this stack needs
  # are registered once, explicitly, with `az provider register`.
  resource_provider_registrations = "none"

  features {}
}
