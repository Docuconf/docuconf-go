# CI plans this root without Google credentials: planning a new service
# calls no Google API. The orders-cloudrun job copies this file into the
# root as an override file; a real plan does not use it.
provider "google" {
  access_token = "ci-plan-without-credentials"
}
