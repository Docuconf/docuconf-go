# Copied into deploy/terraform as an override file by e2e.sh: a new
# discount code. The ConfigMap's name carries a hash of its content, so
# the change rolls the Deployment and the new pods read the new file.
module "orders_config" {
  discounts = { inline = { codes = { WELCOME10 = 10, SPRING25 = 25, E2E50 = 50 } } }
}
