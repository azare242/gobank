#!/usr/bin/env bash
# Generate secrets once; keep the same values while retaining the database PVCs.
set -euo pipefail
cd "$(dirname "$0")/../.."
kubectl apply -f deploy/k8s/base/namespace.yaml
kubectl apply -f deploy/k8s/observability/namespace.yaml
for name in gobank-app-secrets shard-a-db-credentials shard-b-db-credentials coordinator-db-credentials; do
  if [[ -n "$(kubectl get secret "$name" -n gobank --ignore-not-found -o name)" ]]; then
    echo "Secret $name already exists. Existing secrets were retained; refusing to regenerate database passwords." >&2
    exit 1
  fi
done
if [[ -n "$(kubectl get secret grafana-admin -n gobank-monitoring --ignore-not-found -o name)" ]]; then
  echo 'grafana-admin already exists. Existing secrets were retained.' >&2
  exit 1
fi
# Keep generated values out of command arguments and terminal output.
umask 077
secret_file="$(mktemp)"
trap 'rm -f "$secret_file"' EXIT
python3 - "$secret_file" <<'PYTHON'
import base64
import json
import secrets
import sys

def secret(name, namespace, data, kind="Opaque"):
    return {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": namespace}, "type": kind, "stringData": data}

items = []
app = {"JWT_SECRET": base64.b64encode(secrets.token_bytes(32)).decode(), "INTERNAL_TOKEN": secrets.token_hex(32), "FIXTURE_PASSWORD": secrets.token_hex(16)}
for name, key in [("shard-a-db", "SHARD_A_DSN"), ("shard-b-db", "SHARD_B_DSN"), ("coordinator-db", "COORDINATOR_DSN")]:
    password = secrets.token_hex(24)
    items.append(secret(name + "-credentials", "gobank", {"username": "bank", "password": password}, "kubernetes.io/basic-auth"))
    app[key] = f"host={name}-rw.gobank.svc.cluster.local port=5432 user=bank password={password} dbname=bank sslmode=require connect_timeout=5"
items.append(secret("gobank-app-secrets", "gobank", app))
items.append(secret("grafana-admin", "gobank-monitoring", {"username": "admin", "password": secrets.token_hex(24)}))
with open(sys.argv[1], "w") as output:
    json.dump({"apiVersion": "v1", "kind": "List", "items": items}, output)
PYTHON
kubectl create -f "$secret_file"
echo 'Secrets created. Retain them when restarting pods or upgrading images.'
