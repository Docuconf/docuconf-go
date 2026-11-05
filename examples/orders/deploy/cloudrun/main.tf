# The orders service on Cloud Run, from Terraform: the same inputs as
# ../terraform (Kubernetes), set through the module docuconf terraform
# -target cloudrun generates from the contract. The module checks every
# input at plan; docuconf lint terraform checks the plan (README.md).
#
# A Cloud Run secret volume holds one file, and a tls input is a directory,
# so this module has no serving-tls input: Cloud Run ends TLS in front of
# the service, which serves HTTP.

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = { source = "hashicorp/google", version = ">= 6.0" }
  }
}

provider "google" {
  project = var.project
  region  = var.region
}

variable "project" {
  type    = string
  default = "acme-prod"
}

variable "region" {
  type    = string
  default = "us-central1"
}

variable "image" {
  type    = string
  default = "us-docker.pkg.dev/acme-prod/orders/orders-api:2.3.0"
}

variable "environment" {
  type    = string
  default = "prod"
}

# The service account the revision runs as. It must be allowed to access
# each secret (roles/secretmanager.secretAccessor).
variable "service_account" {
  type    = string
  default = "orders-api@acme-prod.iam.gserviceaccount.com"
}

module "orders_config" {
  source = "./modules/orders-config"

  # Secrets are references, never values: Cloud Run reads them when it starts an instance.
  DATABASE_URL = { secret = { secret = "orders-db" } }
  WEBHOOK_KEYS = { secret = { secret = "orders-webhook-keys" } } # "old,new" while rotating

  LOG_LEVEL       = { value = var.environment == "prod" ? "warn" : "debug" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com"] }
  WORKER_COUNT    = { value = 8 }

  # A file input comes from a Secret Manager secret, mounted as one file.
  discounts = { secret = { secret = "orders-discounts" } }
}

resource "google_cloud_run_v2_service" "orders" {
  name     = "orders-api"
  location = var.region

  template {
    service_account = var.service_account

    containers {
      image = var.image
      ports {
        container_port = 8080
      }

      dynamic "env" {
        for_each = module.orders_config.cloudrun_env
        content {
          name  = env.value.name
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.value_from == null ? [] : [env.value.value_from]
            content {
              secret_key_ref {
                secret  = value_source.value.secret_key_ref.secret
                version = value_source.value.secret_key_ref.version
              }
            }
          }
        }
      }

      dynamic "volume_mounts" {
        for_each = module.orders_config.cloudrun_volume_mounts
        content {
          name       = volume_mounts.value.name
          mount_path = volume_mounts.value.mount_path
        }
      }
    }

    dynamic "volumes" {
      for_each = module.orders_config.cloudrun_volumes
      content {
        name = volumes.value.name
        secret {
          secret       = volumes.value.secret.secret
          default_mode = volumes.value.secret.default_mode
          dynamic "items" {
            for_each = volumes.value.secret.items
            content {
              version = items.value.version
              path    = items.value.path
            }
          }
        }
      }
    }
  }
}
