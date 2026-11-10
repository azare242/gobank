# Kubernetes manifests for gobank

The repository has three deployment modes. Each one is self-contained and uses
its own namespace, so you can run them side by side on the same Minikube cluster.

| Directory | Mode | Entry point |
| --- | --- | --- |
| [`sharded/`](sharded/README.md) | Gateway + two shards + PostgreSQL (Assignment 2/3) | `kubectl apply -f deploy/k8s/sharded` |
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
