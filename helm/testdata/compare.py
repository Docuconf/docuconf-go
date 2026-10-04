"""Compares a chart's rendered output with the CUE renderer's golden output.

Usage: compare.py <helm template output> <docuconf render output>
"""
import json
import re
import sys

import yaml

rendered = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
golden = yaml.safe_load(open(sys.argv[2]))

deployment = next(d for d in rendered if d["kind"] == "Deployment")
pod = deployment["spec"]["template"]["spec"]
container = pod["containers"][0]
config_maps = {d["metadata"]["name"]: d for d in rendered if d["kind"] == "ConfigMap"}


def by_name(items):
    return {i["name"]: i for i in items or []}


def same_env(a, b):
    if a == b:
        return True
    # json variables: Helm sorts object keys, CUE keeps declaration order.
    try:
        return json.loads(a["value"]) == json.loads(b["value"]) and a["name"] == b["name"]
    except (KeyError, TypeError, ValueError):
        return False


problems = []
# Structured inline content is serialized by each renderer in its own way
# (SPEC 4.6.1), so its ConfigMap names carry different hashes. Pair the
# ConfigMaps by name without the hash and compare their data parsed;
# string content must match byte for byte, and so must its hash.
def unhashed(name):
    return re.sub(r"-[0-9a-f]{10}$", "", name)


def parsed(key, text):
    if key.endswith(".json"):
        return json.loads(text)
    if key.endswith((".yaml", ".yml")):
        return yaml.safe_load(text)
    return text


renamed = {}  # helm ConfigMap name -> the CUE renderer's name
helm_by_stem = {unhashed(n): cm for n, cm in config_maps.items()}
for cm in golden["configMaps"]:
    want_name = cm["metadata"]["name"]
    got = helm_by_stem.get(unhashed(want_name))
    if got is None or got.get("immutable") is not True:
        problems.append(f"ConfigMap {want_name}: helm {got} cue {cm}")
        continue
    if got["data"] == cm["data"]:
        if got["metadata"]["name"] != want_name:
            problems.append(f"ConfigMap {want_name}: same content, but helm names it {got['metadata']['name']}")
        continue
    if got["data"].keys() == cm["data"].keys() and all(
        parsed(k, got["data"][k]) == parsed(k, cm["data"][k]) for k in cm["data"]
    ):
        renamed[got["metadata"]["name"]] = want_name
        continue
    problems.append(f"ConfigMap {want_name}: helm {got} cue {cm}")
if len(config_maps) != len(golden["configMaps"]):
    problems.append(f"ConfigMaps: helm {sorted(config_maps)} cue {[c['metadata']['name'] for c in golden['configMaps']]}")
for v in pod["volumes"] or []:
    if "configMap" in v and v["configMap"]["name"] in renamed:
        v["configMap"]["name"] = renamed[v["configMap"]["name"]]

helm_env, cue_env = by_name(container["env"]), by_name(golden["env"])
if helm_env.keys() != cue_env.keys():
    problems.append(f"env names differ: helm {sorted(helm_env)} cue {sorted(cue_env)}")
for name in helm_env.keys() & cue_env.keys():
    if not same_env(helm_env[name], cue_env[name]):
        problems.append(f"env {name}: helm {helm_env[name]} cue {cue_env[name]}")

for field, helm_items in (("volumes", pod["volumes"]), ("volumeMounts", container["volumeMounts"])):
    if by_name(helm_items) != by_name(golden[field]):
        problems.append(f"{field} differ:\nhelm {by_name(helm_items)}\ncue  {by_name(golden[field])}")

expected = {t["name"] for t in golden["restartTriggers"] if t["kind"] in ("ConfigMap", "Secret")}
annotations = deployment["metadata"].get("annotations") or {}
got = {n for k, v in annotations.items() if "reloader" in k for n in v.split(",")}
if got != expected:
    problems.append(f"reloader annotations {got} != restart triggers {expected}")

if problems:
    print("\n".join(problems))
    sys.exit(1)
print("parity: env, volumes, mounts, ConfigMaps and restart triggers match the CUE renderer")
