#!/usr/bin/env python3
"""Compares the manifests `pulumi up` rendered (renderYamlToDirectory) with
what `docuconf render` makes of the same inputs in ../values.yaml and
../files.yaml: the container's env, volumes and mounts, and the ConfigMaps.

    check-rendered.py [--by-meaning] <docuconf render output> <rendered directory>

With --by-meaning, ConfigMap content is compared as data, and names with
their content hash masked, each hash checked against its own content: a Go
map has no key order, so a Go program's YAML is sorted.
"""
import hashlib
import json
import pathlib
import re
import sys

import yaml

args = sys.argv[1:]
by_meaning = args[:1] == ["--by-meaning"]
if by_meaning:
    args = args[1:]
want = yaml.safe_load(open(args[0]))
docs = [d for f in sorted(pathlib.Path(args[1]).rglob("*.yaml")) for d in yaml.safe_load_all(f.read_text()) if d]
HASH = re.compile(r"-([0-9a-f]{10})$")


def meaning(cms):
    """ConfigMaps with hashes masked and content parsed; fails on a stale hash."""
    out = []
    for c in cms:
        for content in c["data"].values():
            m = HASH.search(c["name"])
            if m and hashlib.sha256(content.encode()).hexdigest()[:10] != m.group(1):
                sys.exit(f"ConfigMap {c['name']}: its hash is not its content's")
        out.append({"name": HASH.sub("-HASH", c["name"]), "data": {k: yaml.safe_load(v) for k, v in c["data"].items()}, "immutable": c["immutable"]})
    return out


def mask(x):
    """x with the content hashes in ConfigMap names masked."""
    return json.loads(re.sub(r'-[0-9a-f]{10}"', '-HASH"', json.dumps(x)))

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
if by_meaning:
    got["configMaps"], want["configMaps"] = meaning(got["configMaps"]), meaning(want["configMaps"])
    got["volumes"], want["volumes"] = mask(got["volumes"]), mask(want["volumes"])
failed = False
for key in got:
    if got[key] != want[key]:
        failed = True
        print(f"{key} differs from docuconf render:\n  pulumi: {got[key]}\n  render: {want[key]}")
if failed:
    sys.exit(1)
print("the rendered manifests match docuconf render: env, volumes, volumeMounts, configMaps")
