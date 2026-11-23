#!/usr/bin/env bash
# Build the three core Assignment 3 images; optionally publish or load Minikube.
set -euo pipefail
cd "$(dirname "$0")/../.."

registry="${IMAGE_NAMESPACE:-azare242}"
version="${VERSION:-v2}"
push=false
load=false
proxy="${BUILD_PROXY:-}"
while (($#)); do
  case "$1" in
    --push) push=true ;;
    --load) load=true ;;
    --proxy) proxy="${2:?--proxy requires a URL}"; shift ;;
    *) echo "Usage: $0 [--push] [--load] [--proxy http://127.0.0.1:10808]" >&2; exit 2 ;;
  esac
  shift
done

revision="$(git rev-parse HEAD)"
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  revision+="-dirty"
fi
build_args=(--build-arg "VERSION=$version" --build-arg "COMMIT=$revision")
if [[ -n "$proxy" ]]; then
  # RUN steps reach the host proxy; base image pulls and pushes use Docker's
  # daemon proxy, which must be configured separately.
  build_args+=(--network host --build-arg "HTTP_PROXY=$proxy" --build-arg "HTTPS_PROXY=$proxy")
fi

for role in gateway shard client; do
  image="$registry/gobank-$role:$version"
  docker build "${build_args[@]}" --target "$role" --tag "$image" .
  docker image inspect --format '{{.Id}} {{.Config.User}} {{json .Config.Entrypoint}}' "$image"
done

# Publish only after all builds succeeded. Do not reuse published version tags.
for role in gateway shard client; do
  image="$registry/gobank-$role:$version"
  if $push; then
    docker push "$image"
    docker image inspect --format '{{json .RepoDigests}}' "$image"
  fi
  if $load; then
    minikube image load "$image"
  fi
done
