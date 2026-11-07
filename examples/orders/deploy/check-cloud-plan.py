#!/usr/bin/env python3
"""Check that a Pulumi program plans the same container configuration as
the Terraform root of the same target, for the same inputs.

    check-cloud-plan.py ecs|cloudrun|containerapps TERRAFORM_PLAN.json PULUMI_PREVIEW.json

TERRAFORM_PLAN.json is `terraform show -json` of a plan of ../ecs,
../cloudrun or ../containerapps; PULUMI_PREVIEW.json is `pulumi preview
--json` of the Pulumi program for the same target. On ECS the container
definitions must be equal; on Cloud Run the container's env and volume
mounts and the revision's volumes, once both are in one shape: Terraform
writes a nested block as a list of one, in snake_case, and leaves unset
attributes null. On Container Apps, the env, the app's secrets, and which
secret each file path holds: the module mounts files from one Secret
volume with sub_path, which is all the azurerm provider can express, and
the component mounts each file's own volume at its directory.
"""

import json
import re
import sys


def terraform(plan, kind):
    for r in plan.get("resource_changes", []):
        if r["type"] == kind:
            return r["change"]["after"]
    sys.exit(f"the Terraform plan has no {kind}")


def pulumi(preview, kind):
    for s in preview.get("steps", []):
        if s["urn"].split("::")[-2].endswith(kind):
            return s["newState"]["inputs"]
    sys.exit(f"the Pulumi preview has no {kind}")


def camel(k):
    return re.sub(r"_([a-z])", lambda m: m.group(1).upper(), k)


def tidy(x, blocks=False):
    """Drops what is unset (null, "", [], Pulumi's __defaults); with blocks,
    unwraps Terraform's lists of one block and camelCases its keys."""
    if isinstance(x, dict):
        out = {}
        for k, v in x.items():
            if k == "__defaults":
                continue
            v = tidy(v, blocks)
            if v in (None, "", [], {}):
                continue
            out[camel(k) if blocks else k] = v
        return out
    if isinstance(x, list):
        return [tidy(v, blocks) for v in x]
    return x


def unwrap(x, keep):
    """Terraform blocks are lists; the ones Cloud Run has one of are not."""
    if isinstance(x, dict):
        return {k: (unwrap(v[0], keep) if isinstance(v, list) and len(v) == 1 and k not in keep and isinstance(v[0], dict) else unwrap(v, keep)) for k, v in x.items()}
    if isinstance(x, list):
        return [unwrap(v, keep) for v in x]
    return x


def main():
    target, plan_path, preview_path = sys.argv[1:4]
    plan = json.load(open(plan_path))
    preview = json.load(open(preview_path))
    if target == "ecs":
        want = json.loads(terraform(plan, "aws_ecs_task_definition")["container_definitions"])
        got = json.loads(pulumi(preview, "aws:ecs/taskDefinition:TaskDefinition")["containerDefinitions"])
    elif target == "cloudrun":
        # Lists that stay lists, even with one entry.
        keep = {"containers", "env", "volume_mounts", "volumes", "items"}
        t = tidy(unwrap(terraform(plan, "google_cloud_run_v2_service")["template"][0], keep), blocks=True)
        p = tidy(pulumi(preview, "gcp:cloudrunv2/service:Service")["template"])
        pick = lambda tmpl, env: {
            "env": tmpl["containers"][0].get(env, []),
            "volumeMounts": tmpl["containers"][0].get("volumeMounts", []),
            "volumes": tmpl.get("volumes", []),
        }
        want, got = pick(t, "env"), pick(p, "envs")
    elif target == "containerapps":
        t = terraform(plan, "azurerm_container_app")
        p = pulumi(preview, "azure-native:app:ContainerApp")
        tc, pc = t["template"][0]["container"][0], p["template"]["containers"][0]
        env = lambda name, value, secret: {"name": name, "secret": secret} if secret else {"name": name, "value": value}
        tvols = {v["name"]: v for v in t["template"][0].get("volume") or []}
        pvols = {v["name"]: v for v in p["template"].get("volumes") or []}
        want = {
            "env": [env(e["name"], e.get("value"), e.get("secret_name")) for e in tc.get("env") or []],
            "secrets": sorted([{"name": s["name"], "keyVaultUrl": s["key_vault_secret_id"], "identity": s["identity"]} for s in t.get("secret") or []], key=lambda s: s["name"]),
            "files": sorted([{"path": m["path"], "secret": m["sub_path"]} for m in tc.get("volume_mounts") or [] if tvols.get(m["name"], {}).get("storage_type") == "Secret"], key=lambda f: f["path"]),
        }
        got = {
            "env": [env(e["name"], e.get("value"), e.get("secretRef")) for e in pc.get("env") or []],
            "secrets": sorted([{"name": s["name"], "keyVaultUrl": s["keyVaultUrl"], "identity": s["identity"]} for s in p["configuration"].get("secrets") or []], key=lambda s: s["name"]),
            "files": sorted(
                [{"path": m["mountPath"] + "/" + i["path"], "secret": i["secretRef"]} for m in pc.get("volumeMounts") or [] for i in pvols.get(m["volumeName"], {}).get("secrets") or []],
                key=lambda f: f["path"],
            ),
        }
    else:
        sys.exit(f"unknown target {target}")
    if want != got:
        print("Terraform plans:\n" + json.dumps(want, indent=2, sort_keys=True))
        print("Pulumi plans:\n" + json.dumps(got, indent=2, sort_keys=True))
        sys.exit(f"{target}: the Pulumi program and the Terraform root plan different configuration")
    print(f"{target}: the Pulumi program plans what the Terraform root plans")


if __name__ == "__main__":
    main()
