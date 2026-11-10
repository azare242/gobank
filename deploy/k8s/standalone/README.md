# Standalone gobank on Kubernetes

This runs the original single-server API (`cmd/server`) with SQLite on a
persistent volume. It is the simplest of the three deployment modes.

```
NodePort :30081
       |
  gobank-standalone Pod
       |
  PVC -> /app/data/users.db
```

## Build and load the image

The default Docker target already builds `cmd/server`:

```bash
docker build -t gobank:latest .
minikube image load gobank:latest
```

Or build inside Minikube's Docker daemon:

```bash
eval "$(minikube docker-env)"
docker build -t gobank:latest .
```

## Deploy

```bash
kubectl apply -f deploy/k8s/standalone
kubectl get pods -n gobank-standalone -w
```

Wait for `1/1 Running`.

## Try it

```bash
MINIKUBE_IP=$(minikube ip)

curl http://$MINIKUBE_IP:30081/health

curl -s -X POST http://$MINIKUBE_IP:30081/users \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"example-password"}'
```

Log in and use the returned token exactly as described in the repository
`README.md`:

```bash
curl -s -X POST http://$MINIKUBE_IP:30081/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"example-password"}'
```

## Check persistence

```bash
kubectl delete pod -n gobank-standalone -l app=gobank-standalone
kubectl get pods -n gobank-standalone -w
```

After the new Pod is ready, log in with the same user. The account still exists
because `users.db` lives on the PVC.

## Delete everything

```bash
kubectl delete namespace gobank-standalone
```

## Notes

- `replicas: 1` is required: SQLite is a single file and the PVC is
  `ReadWriteOnce`.
- The init container fixes volume ownership because the image runs as UID 10001.
- The secrets in `01-secrets.yaml` are demo values. Replace them.
- For the PostgreSQL variant used by `standalone-compose.yml`, see the repository
  Docker section. This manifest uses SQLite to keep the Kubernetes example small.
