#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

if [[ $# -lt 1 || $# -gt 2 || (${2:-} != "" && ${2:-} != "--start-vms") ]]; then
  echo "Usage: $0 KUBE_CONTEXT [--start-vms]" >&2
  exit 2
fi
context=$1
start_vms=${2:-}
k() { kubectl --context "$context" -n gobank "$@"; }

# Use --start-vms for normal startup of the provisioned cluster.
source scripts/bonus/vm-inventory.sh
for guest in gobank-cp gobank-w1 gobank-w2; do
  if ! read_inventory "$guest"; then
    echo "Provision $guest and verify its libvirt inventory first." >&2
    exit 1
  fi
  state=$(v domstate "$uuid")
  if [[ $state == "shut off" && $start_vms == "--start-vms" ]]; then
    v start "$uuid"
  elif [[ $state != "running" ]]; then
    echo "$guest is $state. For normal startup, pass --start-vms." >&2
    exit 1
  fi
  printf '%s (%s)\n' "$guest" "$uuid"
  v domblklist "$uuid" --details
done

export NO_PROXY="${NO_PROXY:+${NO_PROXY},}${no_proxy:+${no_proxy},}192.168.83.10,192.168.83.11,192.168.83.12,127.0.0.1,localhost"
export no_proxy="$NO_PROXY"
for attempt in {1..30}; do
  if k --request-timeout=5s get --raw /readyz >/dev/null 2>&1; then
    break
  fi
  if [[ $attempt == 30 ]]; then
    echo "Kubernetes API is unavailable in context $context. Check kubeadm setup and kubeconfig." >&2
    exit 1
  fi
  sleep 2
done
k wait --for=condition=Ready node/gobank-cp node/gobank-w1 node/gobank-w2 --timeout=180s
k wait --for=condition=Ready \
  cluster/shard-a-db cluster/shard-b-db cluster/coordinator-db --timeout=600s
for deployment in shard-a shard-b gateway; do
  k rollout status "deployment/$deployment" --timeout=180s
done

printf '\nRunning bonus topology and replication checks.\n'
exec python3 scripts/bonus/demo.py --context "$context"
