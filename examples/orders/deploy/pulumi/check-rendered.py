#!/usr/bin/env python3
"""Compares the manifests `pulumi up` rendered (renderYamlToDirectory) with
what `docuconf render` makes of the same inputs in ../values.yaml and
../files.yaml: the container's env, volumes and mounts, and the ConfigMaps.

    check-rendered.py <docuconf render output> <rendered directory>
"""
import pathlib
import sys

import yaml

want = yaml.safe_load(open(sys.argv[1]))
docs = [d for f in sorted(pathlib.Path(sys.argv[2]).rglob("*.yaml")) for d in yaml.safe_load_all(f.read_text()) if d]

deployments = [d for d in docs if d["kind"] == "Deployment"]
if len(deployments) != 1:
    sys.exit(f"want one Deployment, got {len(deployments)}")
pod = deployments[0]["spec"]["template"]["spec"]
container = pod["containers"][0]
got = {
    "env": container.get("env", []),
    "volumes": pod.get("volumes", []),
    "volumeMounts": container.get("volumeMounts", []),
    "configMaps": sorted(
        ({"name": d["metadata"]["name"], "data": d["data"], "immutable": d.get("immutable", False)} for d in docs if d["kind"] == "ConfigMap"),
        key=lambda c: c["name"],
    ),
}
want["configMaps"] = sorted(
    ({"name": c["metadata"]["name"], "data": c["data"], "immutable": c.get("immutable", False)} for c in want["configMaps"]),
    key=lambda c: c["name"],
)
failed = False
for key in got:
    if got[key] != want[key]:
        failed = True
        print(f"{key} differs from docuconf render:\n  pulumi: {got[key]}\n  render: {want[key]}")
if failed:
    sys.exit(1)
print("the rendered manifests match docuconf render: env, volumes, volumeMounts, configMaps")
