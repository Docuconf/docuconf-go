# The orders service on Amazon ECS (Fargate), from Terraform: the same
# inputs as ../terraform (Kubernetes), set through the module docuconf
# terraform -target ecs generates from the contract. The module checks
# every input at plan; docuconf lint terraform checks the plan (README.md).
#
# ECS mounts no files, so this module has no discounts or serving-tls
# inputs: the service runs without discount codes and serves HTTP, with TLS
# ended at the load balancer.

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 5.0" }
  }
}

provider "aws" {
  region = var.region
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "image" {
  type    = string
  default = "registry.example.com/orders-api:2.3.0"
}

variable "environment" {
  type    = string
  default = "prod"
}

# Where the secrets live. The task execution role must be allowed to read
# them (secretsmanager:GetSecretValue, ssm:GetParameters).
variable "database_url_secret_arn" {
  type    = string
  default = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf"
}

variable "webhook_keys_parameter_arn" {
  type    = string
  default = "arn:aws:ssm:us-east-1:123456789012:parameter/orders/prod/webhook-keys"
}

variable "execution_role_arn" {
  type    = string
  default = "arn:aws:iam::123456789012:role/orders-task-execution"
}

module "orders_config" {
  source = "./modules/orders-config"

  # Secrets are references, never values: ECS reads them when it starts the task.
  DATABASE_URL = { secret = { value_from = var.database_url_secret_arn, json_key = "url" } }
  WEBHOOK_KEYS = { secret = { value_from = var.webhook_keys_parameter_arn } } # "old,new" while rotating

  LOG_LEVEL       = { value = var.environment == "prod" ? "warn" : "debug" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com"] }
  WORKER_COUNT    = { value = 8 }
}

resource "aws_ecs_task_definition" "orders" {
  family                   = "orders-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = var.execution_role_arn

  container_definitions = jsonencode([{
    name                   = "orders-api"
    image                  = var.image
    essential              = true
    user                   = "65532"
    readonlyRootFilesystem = true
    portMappings           = [{ containerPort = 8080, protocol = "tcp" }]
    environment            = module.orders_config.ecs_environment # [{ name, value }]
    secrets                = module.orders_config.ecs_secrets     # [{ name, valueFrom }]
  }])
}
