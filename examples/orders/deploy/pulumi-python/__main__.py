"""The orders service on Kubernetes, from Pulumi: the same inputs as
../values.yaml and ../files.yaml, set through the component docuconf
pulumi -lang python generates from the contract (./orders_api_config).
The component checks every input before anything is rendered, with the
docuconf Python SDK, the engine a Python service runs at boot (README.md).
"""

import pulumi
import pulumi_kubernetes as k8s

from orders_api_config import OrdersApiConfig

config = pulumi.Config()
namespace = config.require("namespace")
image = config.require("image")
environment = config.require("environment")
# The discount codes, written into an immutable, content-hashed ConfigMap:
# changing them rolls the Deployment.
discount_codes = config.get_object("discountCodes") or {"WELCOME10": 10, "SPRING25": 25}

# With renderDir set, the provider writes the manifests there instead of
# applying them, so the program runs without a cluster (CI does this).
render_dir = config.get("renderDir")
provider = k8s.Provider("k8s", render_yaml_to_directory=render_dir) if render_dir else k8s.Provider("k8s")
opts = pulumi.ResourceOptions(provider=provider)

orders = OrdersApiConfig(
    "orders",
    {
        # Secrets are references, never values: the arguments have no value for them.
        "DATABASE_URL": {"secretKeyRef": {"name": "orders-db", "key": "url"}},
        "WEBHOOK_KEYS": {"secretKeyRef": {"name": "orders-webhooks", "key": "keys"}},  # "old,new" while rotating
        "LOG_LEVEL": {"value": "warn" if environment == "prod" else "debug"},
        "ALLOWED_ORIGINS": {"value": ["https://shop.example.com"]},
        "WORKER_COUNT": {"value": config.require_int("workerCount")},
        # File inputs: a cert-manager Certificate, and the discount codes inline.
        "serving-tls": {
            "certificate": {
                "name": "orders-tls",
                "secretName": "orders-tls",
                "dnsNames": ["orders.example.com"],
                "duration": "2160h",
                "renewBefore": "720h",
            }
        },
        "discounts": {"inline": {"codes": discount_codes}},
    },
)

# The component renders the ConfigMaps; the program declares them.
for cm in orders.kubernetes_config_maps:
    k8s.core.v1.ConfigMap(
        cm["name"],
        metadata={"name": cm["name"], "namespace": namespace},
        data=cm["data"],
        immutable=cm.get("immutable"),
        opts=opts,
    )

k8s.apps.v1.Deployment(
    "orders-api",
    metadata={"name": "orders-api", "namespace": namespace},
    spec={
        "selector": {"match_labels": {"app": "orders-api"}},
        "template": {
            # On the pod template, never the Deployment: injectors read the pod's.
            "metadata": {"labels": {"app": "orders-api"}, "annotations": orders.pod_annotations},
            "spec": {
                # The image runs as uid 65532; fsGroup makes the TLS Secret, mounted 0400, readable to it.
                "security_context": {"run_as_non_root": True, "run_as_user": 65532, "fs_group": 65532},
                "containers": [
                    {
                        "name": "orders-api",
                        "image": image,
                        "env": orders.kubernetes_env,
                        "volume_mounts": orders.kubernetes_volume_mounts,
                        "security_context": {
                            "allow_privilege_escalation": False,
                            "read_only_root_filesystem": True,
                            "capabilities": {"drop": ["ALL"]},
                        },
                    }
                ],
                "volumes": orders.kubernetes_volumes,
            },
        },
    },
    opts=opts,
)
