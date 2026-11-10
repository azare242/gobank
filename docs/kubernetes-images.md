# Assignment 3 container images

The core deployment uses three Dockerfile targets: `gateway`, `shard`, and
`client`. They run as UID/GID `10001:10001`. The original default `legacy`
target remains available for Assignment 1. The client binary comes from the
shared build stage, so no separate Dockerfile is needed.

Images include OCI source, revision, and version labels. The same version and
revision are available as `VERSION` and `COMMIT` for application logs/metrics.
A revision ending in `-dirty` includes uncommitted workspace changes.

## Build and publish

From the repository root:

```bash
# Build locally through the V2Ray HTTP proxy.
bash scripts/k8s/build-images.sh --proxy http://127.0.0.1:10808

# Build and push a NEW version after logging into Docker Hub.
VERSION=v3 bash scripts/k8s/build-images.sh --push --proxy http://127.0.0.1:10808

# Or load locally built images into Minikube.
bash scripts/k8s/build-images.sh --load --proxy http://127.0.0.1:10808
```

`IMAGE_NAMESPACE` defaults to `azare242`; `VERSION` defaults to `v2`.
Choose a new version when publishing changed code. The script prints image IDs
and pushed repository digests. Use digest references when pinning a deployment.

`--proxy` forwards HTTP/HTTPS proxy build arguments to package downloads and
uses host networking for build steps on Linux. Base image pulls and image
pushes use Docker's daemon proxy. For this publication, the daemon's HTTP and
HTTPS proxies were both `http://127.0.0.1:10808`.

## Run the client

The client accepts `BANK_PASSWORD` for `-action login` and `BANK_TOKEN` for
banking actions and workloads. Supply the returned login token through a
protected environment file. For Kubernetes, mount an `emptyDir` at `/tmp` and
set `-journal /tmp/<pod-name>.jsonl` so each workload attempt has a fresh writable
journal when the root filesystem is read-only.

```bash
docker run --rm --read-only --tmpfs /tmp:rw,mode=1777 \
  --env-file /path/to/protected-client.env \
  azare242/gobank-client:v2 \
  -gateway http://gateway:3000 -action workload -account A0500 \
  -count 20 -journal /tmp/workload.jsonl
```

Connect the container to the Gateway's Docker network for this example, or use
an externally reachable Gateway URL.

## Published v2 images

The following `linux/amd64` images were built from workspace revision
`6b340faf9475aa3a9afb65130159f1f11e8303d4-dirty` and pushed through V2Ray.

| Image tag | Published digest |
| --- | --- |
| `azare242/gobank-gateway:v2` | `sha256:029228ed33309987b7cd239136c92a569f378c446df8ed89ac6fd8127670ecf2` |
| `azare242/gobank-shard:v2` | `sha256:a2a820c6f9afa19a04ce14cfbf826b2fc1e9471df610a96996c9e8a87c22605e` |
| `azare242/gobank-client:v2` | `sha256:b9d7f419085b450536fe1564317eb5a9b89509734a6918d69b0887656c7fb320` |

For a pinned image reference, use the repository plus `@sha256:...`, for example:

```text
azare242/gobank-gateway@sha256:029228ed33309987b7cd239136c92a569f378c446df8ed89ac6fd8127670ecf2
```

Before publication, all three images passed an isolated Compose smoke test with
PostgreSQL 17. Application containers used read-only root filesystems. The client
logged in and completed ten deposit/withdraw operations and ten cross-shard
transfers. Final balances were `A0500=990`, `A1500=510` (total `1500`), with zero
pending coordinator records and zero prepared transactions. The Gateway's
separate metrics listener responded successfully. Temporary test containers,
networks, and database volumes were removed afterward.
