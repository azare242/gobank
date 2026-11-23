# Assignment 3: Minikube deployment

This setup follows the core assignment plan. It uses plain YAML files and small
Kustomize file lists. The older `deploy/k8s/sharded` example is a separate static
routing demo; do not apply it together with this setup because both use `gobank`.

## What runs

| Resource | Purpose |
| --- | --- |
| `gateway` Deployment, 2–4 Pods | Public API and durable transaction coordinator |
| `shard-a`, `shard-b` Deployments, 2–6 Pods each | Stateless banking applications |
| Headless Service per shard | DNS returns ready shard Pod addresses |
| `shard-a-db`, `shard-b-db` CNPG Clusters, 3 instances each | Primary plus two PostgreSQL standbys per shard |
| `coordinator-db` CNPG Cluster, 2 instances | Coordinator primary plus standby |
| One 2Gi PVC per DB instance | Database data survives Pod replacement |
| Ingress `gobank.local` | External HTTP entry point |
| Optional NodePort `30080` | Fallback external HTTP entry point |
| NetworkPolicies | Restrict application traffic to its own dependencies |
| Optional Prometheus/Grafana | Metrics, dashboard, and alert rules |

CNPG manages PostgreSQL Pods directly; it does not create StatefulSets for these
Cluster resources. The persistent database workload is represented by CNPG
Clusters, Pods, and PVCs. Do not add a second StatefulSet to manage the same DB.

Shard A owns `A0001`–`A1000`; shard B owns `A1001`–`A2000`. Gateway DNS discovery
changes the application targets without changing account ownership. See
[Go service settings](kubernetes-go.md) and [published images](kubernetes-images.md).

## 1. Start Minikube

Install Docker, Minikube, kubectl with Kustomize support, curl, Python 3, and
sha256sum. The published application images are `linux/amd64`. Allow at least
6 CPUs, 12GiB RAM and 40GiB disk for the cluster; 16GiB RAM gives more room for
monitoring and failure experiments.

```bash
minikube start --driver=docker --kubernetes-version=v1.34.3 \
  --cni=calico --cpus=6 --memory=12288 --disk-size=40g
minikube addons enable ingress
minikube addons enable metrics-server
kubectl get nodes
```

Calico is needed to enforce NetworkPolicy. The `standard` StorageClass comes
from Minikube. This is a single-node exercise: the DB replicas survive Pod or
process failure, but they share the same node failure domain.

The manifests use the published `azare242/gobank-*:v2` images. To avoid registry
pulls during the demo, load the already built images:

```bash
minikube image load azare242/gobank-gateway:v2
minikube image load azare242/gobank-shard:v2
minikube image load azare242/gobank-client:v2
```

For a fresh build, see [container image instructions](kubernetes-images.md).
Docker's host proxy is not automatically inherited by Minikube's container
runtime. If image pulls need V2Ray, configure Minikube's runtime proxy or load
images into Minikube. A container's `127.0.0.1:10808` points at itself, not the
host proxy. Keep cluster and service addresses out of the proxy with `NO_PROXY`.

## 2. Install CNPG and create secrets

```bash
bash scripts/k8s/install-cnpg.sh
bash scripts/k8s/create-secrets.sh
```

The operator helper pins and checksums CNPG 1.28.4 before installation. The
secret helper creates random application/database credentials and matching TLS
DSNs. It refuses to overwrite existing secrets. Keep the same secrets when
reusing the database PVCs. No generated passwords belong in a ConfigMap or Git.

CNPG uses its own operand image, `ghcr.io/cloudnative-pg/postgresql:17.7-standard-bookworm`;
the ordinary `postgres` Docker image is not a replacement for it.

## 3. Apply the application

Render first if you want to inspect what will be created:

```bash
kubectl kustomize deploy/k8s/overlays/minikube
kubectl apply -k deploy/k8s/overlays/minikube
kubectl wait -n gobank --for=condition=Ready cluster/shard-a-db --timeout=600s
kubectl wait -n gobank --for=condition=Ready cluster/shard-b-db --timeout=600s
kubectl wait -n gobank --for=condition=Ready cluster/coordinator-db --timeout=600s
kubectl rollout status -n gobank deployment/shard-a --timeout=180s
kubectl rollout status -n gobank deployment/shard-b --timeout=180s
kubectl rollout status -n gobank deployment/gateway --timeout=180s
kubectl get pods,svc,pvc,hpa,pdb,ingress -n gobank
kubectl get clusters.postgresql.cnpg.io -n gobank
```

Application startup retries database work for two minutes. The startup probe
allows three minutes. During slow initial DB bootstrap, an application container
may restart and retry. Liveness checks only `/health`; database outages affect
`/ready`. The shard headless Services publish ready endpoints only.

The shard-map file uses a `subPath` mount. After changing ConfigMaps or Secrets,
restart the application Deployments to reload their files/environment:

```bash
kubectl rollout restart -n gobank deployment/gateway deployment/shard-a deployment/shard-b
```

## 4. Reach the API

```bash
curl -H 'Host: gobank.local' "http://$(minikube ip)/ready"
```

The Ingress routes to HTTP port 3000 only. Metrics port 9090 is not exposed by
it. If the Minikube IP is unreachable on your host, use an appropriate Minikube
tunnel or a local port-forward:

```bash
kubectl port-forward -n gobank service/gateway 3000:80
# In a second terminal:
curl http://127.0.0.1:3000/ready
```

The optional fallback opens NodePort 30080 and adds a matching HTTP ingress
policy. Apply it only when you want this extra external entry point:

```bash
kubectl apply -k deploy/k8s/optional/nodeport
curl "http://$(minikube ip):30080/ready"
```

It uses `externalTrafficPolicy: Local` to preserve the external source address.
The fallback policy allows IPv4 sources on HTTP port 3000; metrics stay private.
Remove the optional resources to close that fallback:

```bash
kubectl delete -k deploy/k8s/optional/nodeport
```

## 5. Check database replication and failover

Each database requires one synchronous standby. With `dataDurability: required`,
writes can block when that standby is unavailable. The three-instance shard
clusters normally retain a standby after primary promotion. The two-instance
coordinator cluster may block writes until its second instance rejoins. Do not
switch to asynchronous replication to hide this temporary unavailability.

List DB instances, identify the primary, and run these queries on it; replace
`<primary-pod>` with the actual Pod name:

```bash
kubectl get pods -n gobank -l cnpg.io/cluster=shard-a-db \
  -L cnpg.io/instanceRole,role
kubectl exec -n gobank <primary-pod> -- psql -U postgres -d bank -c \
  'SELECT pg_is_in_recovery();'
kubectl exec -n gobank <primary-pod> -- psql -U postgres -d bank -c \
  'SELECT application_name,state,sync_state FROM pg_stat_replication;'
kubectl exec -n gobank <primary-pod> -- psql -U postgres -d bank -c \
  'SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database();'
```

Expect one primary per cluster, streaming standbys, and at least one standby
with `sync_state=quorum` for `ANY 1` replication (`sync` is used for priority
replication). After transactions settle, no unexpected prepared transactions
should remain. Repeat for shard B and the coordinator DB.

For the assignment's primary-failure experiment, record balances and transaction
IDs, delete the identified primary Pod, wait for promotion, then repeat the
queries and check balances/transaction decisions. CNPG's `-rw` Service points
the application at the current primary. All database instances have
`max_prepared_transactions=200`; prepared transaction state must survive
promotion before the failover experiment is considered passed.

## 6. Load and autoscaling

```bash
kubectl apply -k deploy/k8s/load
kubectl logs -n gobank -f job/gobank-load
kubectl get hpa -n gobank -w
```

The Job logs in using the fixture Secret, keeps the token out of logs, and runs
30,000 mixed deposit/withdraw operations with a writable journal under `/tmp`.
Each Pod attempt gets its own filename. `restartPolicy: Never` and
`backoffLimit: 0` avoid silently starting another uncertain workload. Do not run
another workload on the same account while checking the CLI balance invariant.

The workload takes several minutes. CPU-based scaling depends on measured load;
if needed, raise the worker count or lower the per-worker interval in
`deploy/k8s/load/job.yaml`, then create a new Job. Metrics-server can take a
minute to produce HPA measurements. Shard B will not scale from this default
same-shard workload; for cross-shard load, add `-destination A1500` to the client
command. Such transfers reduce A0500 and increase A1500, so keep their starting
balances and do not withdraw more than the source owns.

The Job is retained for an hour after completion. Before rerunning, save logs
and reconcile any uncertain operations using their original journal and keys.
The journal uses `emptyDir`: it is lost if its Pod is deleted. Copy it with
`kubectl cp` while the container is still running if it is needed for recovery
or evidence; kubectl cannot copy from a terminated container. Keep operation IDs
and transaction IDs from logs as well. Use the host CLI with a retained journal
when CLI reconciliation must remain possible after a container exits. Then:

```bash
kubectl delete job -n gobank gobank-load
kubectl apply -k deploy/k8s/load
```

Do not reapply the base Deployment replica count during an HPA experiment.
For the one-Gateway coordinator-crash experiment, save and delete its HPA before
scaling to one; restore the HPA from `deploy/k8s/base/gateway-hpa.yaml` afterward.

## 7. Rolling updates

Deployments allow zero unavailable replicas and one surge Pod, wait five seconds
before treating a new Pod as available, and keep five old revisions. A five-second
preStop delay lets endpoint removal propagate; SIGTERM then allows 25 seconds
for Go handlers to drain within a 45-second termination grace period.

Use a newly published version when updating an image:

```bash
kubectl set image -n gobank deployment/shard-a shard=azare242/gobank-shard:v3
kubectl rollout status -n gobank deployment/shard-a --timeout=180s
# If the image cannot start or become ready:
kubectl rollout undo -n gobank deployment/shard-a
kubectl rollout status -n gobank deployment/shard-a --timeout=180s
```

`v3` above is an example; publish it first. Pending cross-shard transactions
remain durable and are recovered by another Gateway. Check balances and
transaction decisions during and after an update.

## 8. Network isolation

Default-deny policies cover ingress and egress in `gobank`. Explicit rules allow
DNS, Ingress/load Job to Gateway, Gateway to shards/coordinator DB, shard A to
its DB, and shard B to its DB. CNPG DB pods can contact the Kubernetes API and
other external dependencies; their ingress is limited to their own DB cluster,
the owning application, CNPG's operator namespace, and Prometheus.

From a running shard A Pod, its own DB should be reachable, and shard B and the
coordinator DB should time out. These commands use tools already in the Debian
application runtime; no debugging Pod has to be added to a Service selector:

```bash
kubectl exec -n gobank deployment/shard-a -- timeout 3 bash -c \
  'echo > /dev/tcp/shard-a-db-rw/5432'
# Expected failure/timeouts:
kubectl exec -n gobank deployment/shard-a -- timeout 3 bash -c \
  'echo > /dev/tcp/shard-b-headless/3000'
kubectl exec -n gobank deployment/shard-a -- timeout 3 bash -c \
  'echo > /dev/tcp/coordinator-db-rw/5432'
```

Also test DB failover after applying the policies. A manifest alone is not proof
that the CNI is enforcing the intended restrictions.

## 9. Optional monitoring

```bash
kubectl apply -k deploy/k8s/observability
kubectl rollout status -n gobank-monitoring deployment/prometheus --timeout=180s
kubectl rollout status -n gobank-monitoring deployment/grafana --timeout=180s
kubectl port-forward -n gobank-monitoring service/prometheus 9091:9090
# In another terminal:
kubectl port-forward -n gobank-monitoring service/grafana 3001:3000
```

Open Prometheus at `http://127.0.0.1:9091` and Grafana at
`http://127.0.0.1:3001`. Grafana's username is `admin`; read its generated password
locally with:

```bash
kubectl get secret grafana-admin -n gobank-monitoring \
  -o jsonpath='{.data.password}' | base64 -d
```

The small Prometheus deployment discovers Pods using a read-only Role in
`gobank`, scrapes app port 9090 and CNPG port 9187, and evaluates alert rules.
Grafana provisions the Prometheus datasource and the GoBank dashboard.
Prometheus and Grafana use `emptyDir` to keep the optional demo lightweight;
monitoring history resets when their Pods are replaced. Banking data uses PVCs.
There is no Alertmanager notification delivery or kube-state-metrics deployment.
View pod/HPA state with kubectl and keep logs as experiment evidence.

This uses plain Prometheus rather than Prometheus Operator, so no PodMonitor or
PrometheusRule CRDs are needed. CNPG's `enablePodMonitor` is intentionally false.
Alert rules are loaded from a ConfigMap and are visible on Prometheus's Alerts
page. Restart Prometheus after changing its configuration/rules:

```bash
kubectl rollout restart -n gobank-monitoring deployment/prometheus
```

For request and transaction logs:

```bash
kubectl logs -n gobank -l app.kubernetes.io/name=gateway --all-containers --prefix --tail=100
kubectl logs -n gobank -l app.kubernetes.io/component=shard --all-containers --prefix --tail=100
```

## 10. Validation and cleanup

Render all entry points without applying them:

```bash
kubectl kustomize deploy/k8s/overlays/minikube
kubectl kustomize deploy/k8s/optional/nodeport
kubectl kustomize deploy/k8s/load
kubectl kustomize deploy/k8s/observability
```

With CNPG installed, ask the API server to validate without saving resources:

```bash
kubectl apply --dry-run=server -k deploy/k8s/overlays/minikube
kubectl apply --dry-run=server -k deploy/k8s/observability
```

Static/schema checks do not establish that the seven failure experiments pass.
Record actual Pod replacement, DB promotion and replication, transaction
recovery, HPA scaling, rollout/rollback, idempotency, and network-policy evidence
on the running cluster using the assignment plan.

Deleting these namespaces removes application resources, Secrets, and database
PVCs. Use only when intentionally discarding this demo's data:

```bash
kubectl delete namespace gobank gobank-monitoring
```

The cluster-wide CNPG operator is retained; other database clusters may use it.
