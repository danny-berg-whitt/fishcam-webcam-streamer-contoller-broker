"""Cross-check k8s manifests for references a schema validator can't see.

kubeconform confirms each resource is well formed, but not that a
configMapKeyRef names a key the ConfigMap actually defines. A missing
non-optional key only shows up at runtime as CreateContainerConfigError.
Secrets are created by `make secret` / `make broker-init`, not stored
here, so secret references are listed for information only.
"""
import glob
import sys

import yaml

docs = []
for path in sorted(glob.glob("k8s/*.yaml")):
    with open(path) as f:
        docs += [(path, d) for d in yaml.safe_load_all(f) if d]

configmaps = {
    d["metadata"]["name"]: set((d.get("data") or {}).keys())
    for _, d in docs
    if d.get("kind") == "ConfigMap"
}

errors = []
for path, d in docs:
    if d.get("kind") != "Deployment":
        continue
    for c in d["spec"]["template"]["spec"]["containers"]:
        for src in c.get("envFrom", []):
            ref = src.get("configMapRef")
            if ref and ref["name"] not in configmaps and not ref.get("optional"):
                errors.append(f"{path}: {c['name']} envFrom ConfigMap {ref['name']!r} is not defined")
        for env in c.get("env", []):
            vf = env.get("valueFrom") or {}
            if "configMapKeyRef" in vf:
                ref = vf["configMapKeyRef"]
                if ref.get("optional"):
                    continue
                if ref["name"] not in configmaps:
                    errors.append(f"{path}: {c['name']}.{env['name']} ConfigMap {ref['name']!r} is not defined")
                elif ref["key"] not in configmaps[ref["name"]]:
                    errors.append(f"{path}: {c['name']}.{env['name']} key {ref['key']!r} is missing from ConfigMap {ref['name']!r}")
            if "secretKeyRef" in vf:
                ref = vf["secretKeyRef"]
                print(f"info: {c['name']}.{env['name']} expects Secret {ref['name']}/{ref['key']} (created outside git)")

# The streamer's /healthz fails during every pause between ffmpeg restarts,
# so its liveness window must outlast the longest pause, or ordinary
# restarts get the pod killed. Both sides live in different files.
def seconds(d):
    import re
    total = 0
    for n, unit in re.findall(r"(\d+(?:\.\d+)?)(ms|s|m|h)", d):
        total += float(n) * {"ms": 0.001, "s": 1, "m": 60, "h": 3600}[unit]
    return total

cm = next((d for _, d in docs if d.get("kind") == "ConfigMap"
           and d["metadata"]["name"] == "webcam-config"), None)
max_backoff = seconds((cm or {}).get("data", {}).get("RESTART_MAX_BACKOFF", "30s"))
for path, d in docs:
    if d.get("kind") != "Deployment":
        continue
    for c in d["spec"]["template"]["spec"]["containers"]:
        probe = c.get("livenessProbe")
        if c["name"] != "streamer" or not probe:
            continue
        window = probe.get("periodSeconds", 10) * probe.get("failureThreshold", 3)
        if window <= max_backoff:
            errors.append(
                f"{path}: streamer liveness window {window}s doesn't outlast "
                f"RESTART_MAX_BACKOFF {max_backoff:g}s; normal restarts would kill the pod")
        else:
            print(f"ok: streamer liveness window {window}s > RESTART_MAX_BACKOFF {max_backoff:g}s")

for e in errors:
    print(f"::error::{e}")
if errors:
    sys.exit(1)
print("ok: every ConfigMap reference resolves")
