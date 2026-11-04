# The orders service on Kubernetes, from Terraform: the same inputs as
# ../values.yaml and ../files.yaml, set through the module docuconf
# terraform generates from the contract. The module checks every input at
# plan; docuconf lint terraform checks the plan (README.md).

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes", version = ">= 2.25" }
  }
}

variable "namespace" {
  type    = string
  default = "orders"
}

variable "image" {
  type    = string
  default = "registry.example.com/orders-api:2.3.0"
}

variable "environment" {
  type    = string
  default = "prod"
}

module "orders_config" {
  source = "./modules/orders-config"

  # Secrets are references, never values: the module has no value attribute for them.
  DATABASE_URL = { secret = { name = "orders-db", key = "url" } }
  WEBHOOK_KEYS = { secret = { name = "orders-webhooks", key = "keys" } } # "old,new" while rotating

  LOG_LEVEL       = { value = var.environment == "prod" ? "warn" : "debug" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com"] }
  WORKER_COUNT    = { value = 8 }

  # File inputs: a cert-manager Certificate, and the discount codes inline.
  serving-tls = { certificate = {
    name         = "orders-tls"
    secret_name  = "orders-tls"
    dns_names    = ["orders.example.com"]
    duration     = "2160h"
    renew_before = "720h"
  } }
  discounts = { inline = { codes = { WELCOME10 = 10, SPRING25 = 25 } } }
}

# The module renders the ConfigMaps; the platform declares them.
resource "kubernetes_config_map_v1" "orders" {
  for_each = module.orders_config.kubernetes_config_maps
  metadata {
    name      = each.key
    namespace = var.namespace
  }
  data      = each.value.data
  immutable = each.value.immutable
}

resource "kubernetes_deployment_v1" "orders" {
  metadata {
    name      = "orders-api"
    namespace = var.namespace
  }
  spec {
    selector { match_labels = { app = "orders-api" } }
    template {
      metadata {
        labels = { app = "orders-api" }
        # On the pod template, never the Deployment: injectors read the pod's.
        annotations = module.orders_config.pod_annotations
      }
      spec {
        container {
          name  = "orders-api"
          image = var.image

          dynamic "env" {
            for_each = module.orders_config.kubernetes_env
            content {
              name  = env.value.name
              value = env.value.value
              dynamic "value_from" {
                for_each = env.value.value_from == null ? [] : [env.value.value_from]
                content {
                  dynamic "secret_key_ref" {
                    for_each = value_from.value.secret_key_ref == null ? [] : [value_from.value.secret_key_ref]
                    content {
                      name = secret_key_ref.value.name
                      key  = secret_key_ref.value.key
                    }
                  }
                  dynamic "config_map_key_ref" {
                    for_each = value_from.value.config_map_key_ref == null ? [] : [value_from.value.config_map_key_ref]
                    content {
                      name = config_map_key_ref.value.name
                      key  = config_map_key_ref.value.key
                    }
                  }
                }
              }
            }
          }

          dynamic "volume_mount" {
            for_each = module.orders_config.kubernetes_volume_mounts
            content {
              name       = volume_mount.value.name
              mount_path = volume_mount.value.mount_path
              read_only  = volume_mount.value.read_only
            }
          }
        }

        dynamic "volume" {
          for_each = module.orders_config.kubernetes_volumes
          content {
            name = volume.value.name
            dynamic "config_map" {
              for_each = volume.value.config_map == null ? [] : [volume.value.config_map]
              content {
                name         = config_map.value.name
                default_mode = config_map.value.default_mode
                dynamic "items" {
                  for_each = config_map.value.items
                  content {
                    key  = items.value.key
                    path = items.value.path
                  }
                }
              }
            }
            dynamic "secret" {
              for_each = volume.value.secret == null ? [] : [volume.value.secret]
              content {
                secret_name  = secret.value.secret_name
                default_mode = secret.value.default_mode
              }
            }
          }
        }
      }
    }
  }
}
