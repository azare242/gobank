#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

profile=gobank-assignment3
cache="$PWD/.tools/k8s-demo-images"
proxy="${MINIKUBE_IMAGE_PROXY:-socks5h://192.168.49.1:10808}"
if (($#)); then
  echo "Usage: $0" >&2
  exit 2
fi

k() { kubectl --context "$profile" "$@"; }
ctr() { docker exec -i "$profile" ctr -n k8s.io "$@"; }

if [[ "$(docker inspect --type container -f '{{.State.Running}}' "$profile" 2>/dev/null)" != true ]]; then
  minikube -p "$profile" start
fi
demo_ip="$(minikube -p "$profile" ip)"
export NO_PROXY="${NO_PROXY:+${NO_PROXY},}${demo_ip},127.0.0.1,localhost"
export no_proxy="$NO_PROXY"
gateway_url="http://${demo_ip}"

mkdir -p "$cache"
temporary_archive=""
trap 'if [[ -n "$temporary_archive" ]]; then rm -f -- "$temporary_archive"; fi' EXIT
images=(
  registry.k8s.io/ingress-nginx/controller:v1.15.1@sha256:594ceea76b01c592858f803f9ff4d2cb40542cae2060410b2c95f75907d659e1
  registry.k8s.io/ingress-nginx/kube-webhook-certgen:v1.6.9@sha256:01038e7de14b78d702d2849c3aad72fd25903c4765af63cf16aa3398f5d5f2dd
  registry.k8s.io/metrics-server/metrics-server:v0.9.0@sha256:d9862115e7c7881280d3d75ca26bda8ffc0fc213315979575bf23ce9826205c0
  docker.io/azare242/gobank-gateway:v2
  docker.io/azare242/gobank-shard:v2
  docker.io/azare242/gobank-client:v2
  ghcr.io/cloudnative-pg/postgresql:17.7-standard-bookworm
)

for image in "${images[@]}"; do
  reference="$image"
  if [[ "$image" == *@sha256:* ]]; then
    reference="${image%%:*}@${image##*@}"
  fi
  archive="$cache/${image//\//_}.tar"
  if ! ctr images ls -q | grep -Fx "$reference" >/dev/null; then
    if [[ -s "$archive" ]]; then
      echo "Restoring $reference"
      ctr images import --local --platform linux/amd64 - < "$archive"
    else
      echo "Pulling $reference through V2RayN"
      docker exec -e "HTTPS_PROXY=$proxy" "$profile" \
        ctr -n k8s.io images pull --local --platform linux/amd64 "$reference"
    fi
  fi
  if [[ ! -s "$archive" ]]; then
    echo "Saving $reference"
    temporary_archive="$(mktemp "$cache/.export.XXXXXX")"
    ctr images export --local --skip-manifest-json --platform linux/amd64 - "$reference" > "$temporary_archive"
    mv -- "$temporary_archive" "$archive"
    temporary_archive=""
  fi
done

k wait --for=condition=Ready node --all --timeout=180s
if ! k -n ingress-nginx get deployment ingress-nginx-controller >/dev/null 2>&1; then
  minikube -p "$profile" addons enable ingress
fi
if ! k -n kube-system get deployment metrics-server >/dev/null 2>&1; then
  minikube -p "$profile" addons enable metrics-server
fi
k -n ingress-nginx rollout status deployment/ingress-nginx-controller --timeout=180s
k -n kube-system rollout status deployment/metrics-server --timeout=180s
k -n gobank wait --for=condition=Ready \
  cluster/shard-a-db cluster/shard-b-db cluster/coordinator-db --timeout=600s
for deployment in shard-a shard-b gateway; do
  k -n gobank rollout status "deployment/$deployment" --timeout=180s
done
curl --fail --silent --show-error --max-time 10 \
  --retry 12 --retry-connrefused --retry-delay 5 --retry-max-time 75 \
  -H 'Host: gobank.local' "$gateway_url/ready"
printf '\n\nRun the demo when ready:\n'
printf 'GATEWAY_URL=%q NO_PROXY=%q no_proxy=%q python3 scripts/k8s/demo.py all --context %q --monitoring\n' \
  "$gateway_url" "$NO_PROXY" "$no_proxy" "$profile"
