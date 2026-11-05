# Copied into deploy/terraform as an override file by e2e.sh: the plan
# must fail on the module's own check, before anything reaches the cluster.
module "orders_config" {
  WORKER_COUNT = { value = 100 }
}
