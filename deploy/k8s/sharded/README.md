# Simple Kubernetes deployment for gobank

These manifests run the sharded gobank setup on Minikube:

```
             NodePort :30080
                   |
              gateway (2 Pods)
              /               \
      shard-a1, shard-a2    shard-b1, shard-b2
             |                      |
           db-a                   db-b

                   db-coordinator
                (cross-shard decisions)
```

The gateway uses `shards.json` to find the four shard URLs. Each shard Pod has its
own Service so the existing static shard map works without changing the Go code.

## What is in this directory

| File | Contents |
| --- | --- |
| `00-namespace.yaml` | `gobank` namespace |
| `01-secrets.yaml` | Database password, internal token, JWT secret, fixture password |
| `02-configmap-shards.yaml` | `shards.json` with the URLs of the shard Services |
| `03-postgres.yaml` | Three Postgres Deployments, Services and PVCs |
| `04-shards.yaml` | Four shard Deployments and four Services |
| `05-gateway.yaml` | Gateway Deployment and NodePort Service |

## 1. Build the images

Minikube cannot see images that only exist in your normal Docker daemon. Use one
of these two options.

Option A, load the images into Minikube:

```bash
docker build --target gateway -t gobank-gateway:assignment2 .
docker build --target shard -t gobank-shard:assignment2 .

minikube image load gobank-gateway:assignment2
minikube image load gobank-shard:assignment2
```

Option B, build directly inside Minikube's Docker daemon:

```bash
eval "$(minikube docker-env)"
docker build --target gateway -t gobank-gateway:assignment2 .
docker build --target shard -t gobank-shard:assignment2 .
```

The manifests use `imagePullPolicy: IfNotPresent`, so Kubernetes will use the
local images.

## 2. Change the demo secrets

`01-secrets.yaml` contains fake values so the exercise works out of the box.
Replace them before using this anywhere other than a local Minikube cluster:

- `INTERNAL_TOKEN`: at least 32 bytes
- `JWT_SECRET`: base64 of at least 32 random bytes, for example
  `openssl rand -base64 32`
- `FIXTURE_PASSWORD`: 8 to 72 bytes
- `POSTGRES_PASSWORD`: keep it URL-safe because it is inserted into the DSN

## 3. Deploy

```bash
kubectl apply -f deploy/k8s/sharded
```

The files are numbered so the namespace is created first. The shard Pods may
restart a few times while Postgres starts; that is expected.

Watch the Pods:

```bash
kubectl get pods -n gobank -w
```

Wait until all Pods show `1/1 Running`.

## 4. Try it

The gateway is exposed with a NodePort:

```bash
curl http://$(minikube ip):30080/health
curl http://$(minikube ip):30080/ready
```

Log in and get a token:

```bash
curl -s -X POST http://$(minikube ip):30080/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"A0500","password":"gobank-demo-password"}'
```

See `README.md` in the repository root for the other API calls, or use the
`cmd/client` program against `http://$(minikube ip):30080`.

## 5. Useful commands

```bash
kubectl get pods,svc,pvc -n gobank
kubectl logs -n gobank deploy/gateway
kubectl logs -n gobank deploy/shard-a1
kubectl describe pod -n gobank <pod-name>
```

Restart a shard replica to see Kubernetes recreate it:

```bash
kubectl delete pod -n gobank -l app=shard-a1
```

Delete everything:

```bash
kubectl delete namespace gobank
```

## Deliberate simplifications

This is a learning deployment, not a production one:

- One Postgres Pod per database, no standby and no automatic failover.
  Data still lives on a PVC, so it survives Pod deletion.
- No Ingress. The gateway uses a NodePort (`30080`).
- No HPA. Each shard has exactly two fixed replicas, one in each Deployment.
- No NetworkPolicy. All Pods can talk to each other.
- The shard map is static. Scaling means editing a Deployment and `shards.json`.
- Secrets are checked into YAML for the demo. Do not do that in real projects.
