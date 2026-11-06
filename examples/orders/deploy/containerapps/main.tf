# The orders service on Azure Container Apps, from Terraform: the same
# inputs as ../terraform (Kubernetes), set through the module docuconf
# terraform -target containerapps generates from the contract. The module
# checks every input at plan; docuconf lint terraform checks the plan
# (README.md).
#
# A Container Apps secret holds one file, and a tls input is a directory,
# so this module has no serving-tls input: Container Apps ingress ends TLS
# in front of the app, which serves HTTP.

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    azurerm = { source = "hashicorp/azurerm", version = ">= 4.0" }
  }
}

provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
}

variable "subscription_id" {
  type    = string
  default = "00000000-0000-0000-0000-000000000000"
}

variable "resource_group" {
  type    = string
  default = "orders"
}

variable "environment_id" {
  description = "The Container Apps environment the app runs in."
  type        = string
  default     = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/orders/providers/Microsoft.App/managedEnvironments/orders"
}

variable "image" {
  type    = string
  default = "ordersacr.azurecr.io/orders-api:2.3.0"
}

variable "environment" {
  type    = string
  default = "prod"
}

# The identity that reads the Key Vault secrets. It must be allowed to read
# each (the Key Vault Secrets User role) before the first revision starts,
# which is why it is user-assigned: a system-assigned identity exists only
# once the app does.
variable "identity_id" {
  type    = string
  default = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/orders/providers/Microsoft.ManagedIdentity/userAssignedIdentities/orders-api"
}

variable "key_vault" {
  type    = string
  default = "https://orders-kv.vault.azure.net"
}

module "orders_config" {
  source = "./modules/orders-config"

  # Secrets are references, never values: Container Apps reads them from
  # Key Vault. A versionless ID follows the secret's latest version.
  DATABASE_URL = { secret = { key_vault_secret_id = "${var.key_vault}/secrets/orders-db", identity = var.identity_id } }
  WEBHOOK_KEYS = { secret = { key_vault_secret_id = "${var.key_vault}/secrets/orders-webhook-keys", identity = var.identity_id } } # "old,new" while rotating

  LOG_LEVEL       = { value = var.environment == "prod" ? "warn" : "debug" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com"] }
  WORKER_COUNT    = { value = 8 }

  # A file input comes from a Key Vault secret, mounted as one file.
  discounts = { secret = { key_vault_secret_id = "${var.key_vault}/secrets/orders-discounts", identity = var.identity_id } }
}

resource "azurerm_container_app" "orders" {
  name                         = "orders-api"
  container_app_environment_id = var.environment_id
  resource_group_name          = var.resource_group
  revision_mode                = "Single"
  # The env, secrets and mounts come from dynamic blocks, which a plan's
  # configuration leaves out: this reference is what binds the app to the
  # module for docuconf lint.
  tags = { app = module.orders_config.contract.name }

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity_id]
  }

  dynamic "secret" {
    for_each = module.orders_config.containerapp_secrets
    content {
      name                = secret.value.name
      key_vault_secret_id = secret.value.key_vault_secret_id
      identity            = secret.value.identity
    }
  }

  ingress {
    external_enabled = true
    target_port      = 8080
    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }

  template {
    container {
      name   = "orders-api"
      image  = var.image
      cpu    = 0.5
      memory = "1Gi"

      dynamic "env" {
        for_each = module.orders_config.containerapp_env
        content {
          name        = env.value.name
          value       = env.value.value
          secret_name = env.value.secret_name
        }
      }

      dynamic "volume_mounts" {
        for_each = module.orders_config.containerapp_volume_mounts
        content {
          name     = volume_mounts.value.name
          path     = volume_mounts.value.path
          sub_path = volume_mounts.value.sub_path
        }
      }
    }

    dynamic "volume" {
      for_each = module.orders_config.containerapp_volumes
      content {
        name         = volume.value.name
        storage_type = volume.value.storage_type
      }
    }
  }
}
