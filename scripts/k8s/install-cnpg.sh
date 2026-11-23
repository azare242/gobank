#!/usr/bin/env bash
set -euo pipefail
version=1.28.4
checksum=8f24b9163df8ff771fc398de885e6c4052f31a1289f921f9d23024eff5e8ebbe
manifest="$(mktemp)"
trap 'rm -f "$manifest"' EXIT
curl --fail --location --retry 3 \
  "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v${version}/releases/cnpg-${version}.yaml" \
  --output "$manifest"
printf '%s  %s\n' "$checksum" "$manifest" | sha256sum --check --status
kubectl apply --server-side -f "$manifest"
kubectl wait --for=condition=Established crd/clusters.postgresql.cnpg.io --timeout=120s
kubectl rollout status deployment/cnpg-controller-manager -n cnpg-system --timeout=180s
