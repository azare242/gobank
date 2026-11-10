# Three-node Raft cluster on Kubernetes

This runs the Assignment 1 bonus cluster (`cmd/cluster-server`) with three
voters: `a`, `b`, and `c`.

```
   NodePort :30082      :30083      :30084
              |            |            |
          cluster-a    cluster-b    cluster-c
              \____________|____________/
                       Raft :7000
                    (headless Services)
```

Each node has its own Deployment, PVC (`/app/data`) and headless Service. The
headless Service gives every node a stable DNS name for the Raft peer list.

## Build and load the image

The `gobank:latest` image already contains `cluster-server`:

```bash
docker build -t gobank:latest .
minikube image load gobank:latest
```

## Deploy

```bash
kubectl apply -f deploy/k8s/cluster
kubectl get pods -n gobank-cluster -w
```

A brand-new cluster may restart a node once or twice while Kubernetes publishes
the headless Service DNS record. Wait until all three Pods are `1/1 Running`.

## Find the leader

Each node is exposed on its own NodePort:

```bash
MINIKUBE_IP=$(minikube ip)

curl -s http://$MINIKUBE_IP:30082/cluster/status
curl -s http://$MINIKUBE_IP:30083/cluster/status
curl -s http://$MINIKUBE_IP:30084/cluster/status
```

Look at the `"role"` field. Exactly one node should say `"Leader"`; the other two
say `"Follower"`. Only the leader accepts writes. A follower returns HTTP 503
with the leader URL in the response, and `cmd/client` retries against the list of
servers.

## Use the client

Point the client at all three NodePorts:

```bash
export BANK_SERVERS="http://$MINIKUBE_IP:30082,http://$MINIKUBE_IP:30083,http://$MINIKUBE_IP:30084"
export BANK_PASSWORD='demo-password-123'

./bin/bank-client -servers "$BANK_SERVERS" -action register -username demo -id signup-demo-1
login=$(./bin/bank-client -servers "$BANK_SERVERS" -action login -username demo)
export BANK_TOKEN=$(jq -er '.access_token' <<< "$login")
./bin/bank-client -servers "$BANK_SERVERS" -action balance
```

Build the client first with `go build -o bin/bank-client ./cmd/client`.

## Try self-healing

Delete a node's Pod:

```bash
kubectl delete pod -n gobank-cluster -l app=cluster-a
kubectl get pods -n gobank-cluster -w
```

The Deployment creates a replacement. Its PVC keeps the Raft log and application
database, so the replacement rejoins with its old state. The other two voters can
continue to form a majority while it restarts. Do not delete the PVC unless you
want to discard that node's data.

## Delete everything

```bash
kubectl delete namespace gobank-cluster
```

## Notes

- All three nodes must use the same `JWT_SECRET`, `JWT_ISSUER`, and
  `JWT_AUDIENCE`. They read the same Secret.
- `bootstrap: true` appears only in `a.json`. It applies only when node A has no
  existing Raft state.
- Readiness uses `/health` on purpose. `/ready` commits a Raft barrier, so using
  it as a periodic probe would create continuous replicated writes.
- The Raft `raft` address is the node's headless Service DNS name, not a Pod IP,
  so the configuration survives Pod replacements.
- On single-node Minikube all three Pods share one host. This demonstrates the
  Raft behaviour, but it is not a real multi-node failure domain.
- The secrets in `01-secrets.yaml` are demo values. Replace them.
