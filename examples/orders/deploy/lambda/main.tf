# The orders service on AWS Lambda, from Terraform: the same inputs as
# ../terraform (Kubernetes), set through the module docuconf terraform
# -target lambda generates from the contract. The module checks every
# input at plan; docuconf lint terraform checks the plan (README.md).
#
# Lambda mounts no files, so this module has no discounts or serving-tls
# inputs, and it resolves no secret references in its environment: the
# secrets are injected as their ARNs, and the function reads them through
# the AWS Parameters and Secrets Lambda Extension.

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
  default = "123456789012.dkr.ecr.us-east-1.amazonaws.com/orders-api:2.3.0"
}

variable "environment" {
  type    = string
  default = "prod"
}

# Where the secrets live. The function's execution role must be allowed to
# read them (secretsmanager:GetSecretValue, ssm:GetParameter).
variable "database_url_secret_arn" {
  type    = string
  default = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf"
}

variable "webhook_keys_parameter_arn" {
  type    = string
  default = "arn:aws:ssm:us-east-1:123456789012:parameter/orders/prod/webhook-keys"
}

variable "role_arn" {
  type    = string
  default = "arn:aws:iam::123456789012:role/orders-api"
}

module "orders_config" {
  source = "./modules/orders-config"

  # Secrets are injected as their ARNs: the function reads them at start.
  DATABASE_URL = { injected = { provider = "aws-parameters-and-secrets", ref = var.database_url_secret_arn } }
  WEBHOOK_KEYS = { injected = { provider = "aws-parameters-and-secrets", ref = var.webhook_keys_parameter_arn } }

  LOG_LEVEL       = { value = var.environment == "prod" ? "warn" : "debug" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com"] }
  WORKER_COUNT    = { value = 8 }
}

resource "aws_lambda_function" "orders" {
  function_name = "orders-api"
  role          = var.role_arn
  package_type  = "Image"
  image_uri     = var.image
  timeout       = 30

  environment {
    variables = module.orders_config.lambda_environment
  }
}
