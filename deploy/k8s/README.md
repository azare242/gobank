# Kubernetes manifests for gobank

For the core Assignment 3 deployment, use the [Minikube runbook](../../docs/kubernetes-runbook.md).
It uses DNS discovery, CloudNativePG replication, probes, HPA, network policies,
and the published v2 images.

The older examples below are retained for comparison. The Assignment 3 setup
and the older sharded example both use `gobank`; apply only one of them.

| Directory | Mode | Entry point |
| --- | --- | --- |
| [`overlays/minikube/`](../../docs/kubernetes-runbook.md) | Core Assignment 3 with replicated PostgreSQL | `kubectl apply -k deploy/k8s/overlays/minikube` |
| [`sharded/`](sharded/README.md) | Older static-routing, single-instance DB example | `kubectl apply -f deploy/k8s/sharded` |
| [`standalone/`](standalone/README.md) | Original single server (`cmd/server`) with SQLite (Assignment 1) | `kubectl apply -f deploy/k8s/standalone` |
| [`cluster/`](cluster/README.md) | Three-node Raft cluster (`cmd/cluster-server`, Assignment 1 bonus) | `kubectl apply -f deploy/k8s/cluster` |

## Images

All modes use locally loaded Minikube images:

```bash
# Standalone and Raft cluster (default Docker target)
docker build -t gobank:latest .
minikube image load gobank:latest

# Sharded mode
docker build --target gateway -t gobank-gateway:assignment2 .
docker build --target shard -t gobank-shard:assignment2 .
minikube image load gobank-gateway:assignment2
minikube image load gobank-shard:assignment2
```

Alternatively, run `eval "$(minikube docker-env)"` and build directly inside
Minikube's Docker daemon.

## NodePorts

| Mode | Port |
| --- | --- |
| Sharded gateway | `30080` |
| Standalone server | `30081` |
| Raft node A | `30082` |
| Raft node B | `30083` |
| Raft node C | `30084` |

Access them with `http://$(minikube ip):<port>`.

## Cleanup

```bash
kubectl delete namespace gobank
kubectl delete namespace gobank-standalone
kubectl delete namespace gobank-cluster
```

Each README in the subdirectories has build, deploy, test, and cleanup details.
