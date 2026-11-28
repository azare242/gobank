#!/usr/bin/env python3
"""Show application placement and synchronous replication on the bonus cluster."""
import argparse
import datetime
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/k8s"))
from verify import kubectl  # noqa: E402

WORKERS = {"gobank-w1", "gobank-w2"}
APPLICATIONS = ("gateway", "shard-a", "shard-b")
DATABASES = ("shard-a-db", "shard-b-db", "coordinator-db")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def ready(pod):
    return not pod["metadata"].get("deletionTimestamp") and any(
        condition["type"] == "Ready" and condition["status"] == "True"
        for condition in pod.get("status", {}).get("conditions", [])
    )


def topology(k):
    nodes = json.loads(k("get", "nodes", "-o", "json"))["items"]
    labels = {node["metadata"]["name"]: node["metadata"].get("labels", {}) for node in nodes}
    require(set(labels) == WORKERS | {"gobank-cp"}, "Expected gobank-cp, gobank-w1 and gobank-w2")
    require("node-role.kubernetes.io/control-plane" in labels["gobank-cp"],
            "gobank-cp must be the control plane")
    require({name for name, values in labels.items() if values.get("gobank.io/worker") == "true"}
            == WORKERS, "Expected both workers to have gobank.io/worker=true")
    placements = {}
    for app in APPLICATIONS:
        pods = json.loads(k("get", "pods", "-l", f"app.kubernetes.io/name={app}", "-o", "json"))["items"]
        placements[app] = {pod["metadata"]["name"]: pod["spec"]["nodeName"]
                           for pod in pods if ready(pod)}
        require(set(placements[app].values()) == WORKERS,
                f"{app}: expected ready replicas on both workers, found {placements[app]}")
    return {"nodes": sorted(labels), "ready_application_pods": placements}


def sql(k, pod, query):
    return json.loads(k("exec", pod, "-c", "postgres", "--", "psql", "-X", "-U",
                        "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-Atc", query))


def replication(k):
    view = {}
    volumes = set()
    paths = set()
    for name in DATABASES:
        cluster = json.loads(k("get", "cluster", name, "-o", "json"))
        primary = cluster["status"]["currentPrimary"]
        pods = json.loads(k("get", "pods", "-l", f"cnpg.io/cluster={name}", "-o", "json"))["items"]
        instances = {}
        for pod in pods:
            if not any(c["name"] == "postgres" for c in pod["spec"]["containers"]):
                continue
            instance = pod["metadata"]["name"]
            node = pod["spec"].get("nodeName")
            require(ready(pod) and node in WORKERS, f"{instance}: expected a ready instance on a worker")
            claim_name = next(v["persistentVolumeClaim"]["claimName"] for v in pod["spec"]["volumes"]
                              if v["name"] == "pgdata")
            claim = json.loads(k("get", "pvc", claim_name, "-o", "json"))
            volume_name = claim["spec"]["volumeName"]
            pv = json.loads(k("get", "pv", volume_name, "-o", "json"))["spec"]
            path = pv.get("local", {}).get("path")
            require(claim["status"]["phase"] == "Bound" and pv["storageClassName"] == "gobank-local"
                    and path and volume_name not in volumes and (node, path) not in paths,
                    f"{instance}: expected its own bound local volume")
            volumes.add(volume_name)
            paths.add((node, path))
            instances[instance] = {"worker": node, "claim": claim_name, "volume": volume_name,
                                   "path": path, "in_recovery": sql(k, instance, "SELECT to_json(pg_is_in_recovery())")}
        require(len(instances) == 3 and primary in instances, f"{name}: expected three instances")
        require({pod for pod, data in instances.items() if not data["in_recovery"]} == {primary},
                f"{name}: expected only {primary} to be writable")
        require(set(data["worker"] for data in instances.values()) == WORKERS
                and sum(data["worker"] == instances[primary]["worker"] for data in instances.values()) == 2,
                f"{name}: expected one standby on the primary worker and one on the other worker")

        settings = sql(k, primary, "SELECT json_build_object('commit', current_setting('synchronous_commit'), "
                       "'standbys', current_setting('synchronous_standby_names'))")
        require(settings["commit"] == "on" and re.match(r"^(?:ANY|FIRST)\s+2\s*\(", settings["standbys"], re.I),
                f"{name}: expected two synchronous standby acknowledgments, found {settings}")
        streams = sql(k, primary, "SELECT coalesce(json_agg(t), '[]') FROM "
                      "(SELECT application_name, state, sync_state, flush_lsn, replay_lsn "
                      "FROM pg_stat_replication) t")
        streaming = {row["application_name"] for row in streams if row["state"] == "streaming"
                     and row["sync_state"] in ("sync", "quorum") and row["flush_lsn"] and row["replay_lsn"]}
        require(streaming == set(instances) - {primary},
                f"{name}: expected both standbys streaming synchronously, found {streams}")
        view[name] = {"primary": primary, "instances": instances, "settings": settings, "streams": streams}
    return view


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scene", nargs="?", default="all", choices=("topology", "replication", "all"))
    parser.add_argument("--context", required=True, help="kubectl context for the three-VM cluster")
    parser.add_argument("--namespace", default="gobank")
    args = parser.parse_args()

    def k(*command):
        return kubectl(args.context, args.namespace, *command)

    timestamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    output = ROOT / ".bonus" / "evidence" / f"{timestamp}.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    result = {"context": args.context, "namespace": args.namespace, "checks": {}}
    try:
        for check in (topology, replication):
            if args.scene in ("all", check.__name__):
                evidence = check(k)
                result["checks"][check.__name__] = evidence
                print(f"\n{check.__name__}: passed", flush=True)
                print(json.dumps(evidence, indent=2))
        result["status"] = "passed"
    except (RuntimeError, KeyError, StopIteration, ValueError, OSError, subprocess.SubprocessError) as error:
        result.update(status="failed", error=str(error))
        print(f"Bonus demo failed: {error}", file=sys.stderr)
    output.write_text(json.dumps(result, indent=2) + "\n")
    print(f"Evidence: {output}")
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
