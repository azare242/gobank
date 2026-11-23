# CloudNativePG operator pin

The Assignment 3 files use CloudNativePG **1.28.4**. The install helper downloads
its release-tagged manifest, verifies this SHA-256, and applies it server-side:

```text
URL: https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v1.28.4/releases/cnpg-1.28.4.yaml
SHA256: 8f24b9163df8ff771fc398de885e6c4052f31a1289f921f9d23024eff5e8ebbe
```

Run `bash scripts/k8s/install-cnpg.sh` after creating Minikube. Set
`HTTPS_PROXY=http://127.0.0.1:10808` if the download needs V2Ray; keep the Kubernetes
API address in `NO_PROXY` so kubectl can reach the local cluster directly.

The database manifest uses the documented synchronous replication fields:
`method: any`, `number: 1`, `dataDurability: required`. See the official
[replication guide](https://cloudnative-pg.io/docs/1.28/replication/),
[API reference](https://cloudnative-pg.io/docs/1.28/cloudnative-pg.v1/), and
[network ports](https://cloudnative-pg.io/docs/1.28/security/).
