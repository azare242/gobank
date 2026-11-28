#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 2 && $2 =~ ^gobank-w[12]$ ]] || {
  echo "Usage: $0 KUBE_CONTEXT gobank-w1|gobank-w2" >&2; exit 2;
}
source "$(dirname "$0")/vm-inventory.sh"
read_inventory "$2"
assert_off
k() { kubectl --context "$1" -n gobank "${@:2}"; }
k "$1" get validatingadmissionpolicybinding gobank-fence-before-promotion >/dev/null
k "$1" cordon "$domain"
for cluster in shard-a-db shard-b-db coordinator-db; do
  target=$(k "$1" get cluster "$cluster" -o jsonpath='{.status.targetPrimary}')
  [[ $target == "$cluster"-* ]] || continue
  node=$(k "$1" get pod "$target" -o jsonpath='{.spec.nodeName}')
  if [[ $node == "$domain" ]]; then
    assert_off
    k "$1" patch configmap gobank-fenced-primaries --type=merge \
      -p "{\"data\":{\"$cluster\":\"$target\"}}"
  fi
done
