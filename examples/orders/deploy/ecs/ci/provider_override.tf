# CI plans this root without AWS credentials: planning a new task
# definition calls no AWS API. The orders-ecs job copies this file into
# the root as an override file; a real plan does not use it.
provider "aws" {
  access_key                  = "ci"
  secret_key                  = "ci"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}
