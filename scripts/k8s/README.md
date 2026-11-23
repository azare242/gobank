# Run the Kubernetes demo

## Start the existing assignment profile

From the repository root:

```bash
./scripts/k8s/start-demo.sh
```

After the readiness check, the script prints the Python command with the
Gateway URL and proxy bypass settings. Run that command yourself when ready.
Replace `all` with `banking` for a single smoke test or with `5` for the
autoscaling scene alone.

The printed command includes `--monitoring`. With the observability stack
deployed, the demo opens these local dashboards and keeps them available until
the demo exits:

- Grafana GoBank dashboard at `http://127.0.0.1:3001/d/gobank`
- Prometheus query UI at `http://127.0.0.1:9091/query`. Use
  `sum by (service) (rate(gobank_http_requests_total[1m]))` to show request traffic.

Open the dashboards before pressing Enter for the autoscaling scene.
For Grafana, use username `admin` and read the password locally before presenting:

```bash
kubectl --context gobank-assignment3 -n gobank-monitoring get secret grafana-admin \
  -o jsonpath='{.data.password}' | base64 -d
```

If the monitoring stack is missing, apply it once with
`kubectl --context gobank-assignment3 apply -k deploy/k8s/observability`.
Omit `--monitoring` when running a demo without dashboards.

The setup script uses the existing `gobank-assignment3` profile and deployment.
It saves the Ingress, Metrics Server, GoBank, and PostgreSQL images in
`.tools/k8s-demo-images/` on the host and restores missing node images from
those archives. On a cache miss it uses the V2RayN relay at
`socks5h://192.168.49.1:10808`. Set `MINIKUBE_IMAGE_PROXY` if the node reaches
your proxy at a different address. Keep the cache directory between sessions.

## Manual setup

Run these commands from the repository root. They target the running Minikube
profile named `minikube`. Use a demo deployment with Calico to enforce
NetworkPolicy, and leave other test workloads stopped while the demo checks
fixture balances. For cluster setup and image requirements, see the
[Minikube runbook](../../docs/kubernetes-runbook.md).

## 1. Deploy GoBank

Use the same context for the setup helpers and the demo. If you use a local
proxy, add the Minikube IP and loopback addresses to both proxy bypass variables.

```bash
kubectl config use-context minikube
DEMO_IP="$(minikube -p minikube ip)"
export NO_PROXY="${NO_PROXY:+${NO_PROXY},}${DEMO_IP},127.0.0.1,localhost"
export no_proxy="$NO_PROXY"

kubectl --context minikube get nodes
minikube -p minikube addons enable ingress
minikube -p minikube addons enable metrics-server

bash scripts/k8s/install-cnpg.sh
# Run once on a fresh deployment. Reuse existing Secrets with existing PVCs.
bash scripts/k8s/create-secrets.sh
kubectl --context minikube apply -k deploy/k8s/overlays/minikube
```

If GoBank is already deployed, start with the readiness checks below.

```bash
kubectl --context minikube -n gobank wait --for=condition=Ready \
  cluster/shard-a-db cluster/shard-b-db cluster/coordinator-db --timeout=600s
kubectl --context minikube -n gobank rollout status deployment/shard-a --timeout=180s
kubectl --context minikube -n gobank rollout status deployment/shard-b --timeout=180s
kubectl --context minikube -n gobank rollout status deployment/gateway --timeout=180s
kubectl --context minikube -n gobank get pods,svc,ingress,hpa
```

## 2. Check the gateway

In the same terminal, point the demo at the Ingress and check for HTTP 200.
The script sends the `gobank.local` host header automatically.

```bash
export GATEWAY_URL="http://${DEMO_IP}"
curl --fail --show-error --max-time 10 -H 'Host: gobank.local' "$GATEWAY_URL/ready"
```

If the Ingress address is unreachable, start a port-forward in a second terminal.

```bash
kubectl --context minikube -n gobank port-forward service/gateway 3000:80
```

Then use `export GATEWAY_URL=http://127.0.0.1:3000` and repeat the readiness
check. Prefer Ingress for the full demo because a Service port-forward can
disconnect when its selected Gateway Pod is replaced during coordinator recovery.

## 3. Run the scenes

Start with the deployment and replication scenes to confirm the setup.

```bash
python3 scripts/k8s/demo.py deployment --context minikube
python3 scripts/k8s/demo.py replication --context minikube
python3 scripts/k8s/demo.py all --context minikube
```

The full demo pauses between scenes. Add `--no-pause` for an unattended run.
Select `banking`, `security`, or a number from `1` through `7` to run a single
scene. The numbered scenes write JSON evidence under `scripts/k8s/evidence/`.
The full demo changes fixture balances, replaces Pods and tests rollback, so
run it against the assignment deployment. See the
[scene list](../../docs/kubernetes-runbook.md#live-demo) for the numbered mapping.

If `kubectl` reports an unreachable API, check `kubectl config current-context`
and run `minikube -p minikube status`. If the `gobank` namespace has no
Deployments or Services, complete step 1 on the selected profile. If autoscaling
fails, check that `kubectl --context minikube -n gobank top pods` returns CPU
measurements before retrying scene 5.
