# CI plans this root without Azure credentials. The azurerm provider gets a
# token and lists resource providers before any plan, so the orders-cloud
# job points it (ARM_METADATA_HOSTNAME) at ci/fakeazure, which answers
# those calls; planning a new app calls nothing else. The job copies this
# file into the root as an override file; a real plan does not use it.
provider "azurerm" {
  tenant_id                       = "00000000-0000-0000-0000-000000000000"
  client_id                       = "00000000-0000-0000-0000-000000000000"
  client_secret                   = "ci-plan-without-credentials"
  resource_provider_registrations = "none"
}
