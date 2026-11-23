# Local secrets

Run `bash scripts/k8s/create-secrets.sh` once after starting the cluster. It
creates matching CNPG application-role passwords and application DSNs, JWT and
internal tokens, the fixture login password, and the Grafana administrator
password. Only random values are used; no generated secrets are committed.

The helper refuses to overwrite existing secrets. Retain secrets with the
corresponding database PVCs. Rotating a database password requires updating the
PostgreSQL role and application DSN together; rerunning a bootstrap script is
not a password rotation procedure. Export any backups to protected storage
outside Git. Files in this directory other than this README are ignored.

To read the fixture password locally (the output is sensitive):

```bash
kubectl get secret gobank-app-secrets -n gobank \
  -o jsonpath='{.data.FIXTURE_PASSWORD}' | base64 -d
```
