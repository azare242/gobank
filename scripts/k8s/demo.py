#!/usr/bin/env python3
"""Present the core Kubernetes assignment on an isolated demo deployment."""
import argparse
from contextlib import ExitStack, contextmanager
import json
import os
import subprocess
from tempfile import TemporaryFile
import time
import uuid

import experiments as bank


TITLES = {
    "deployment": "Kubernetes workloads and external entry point",
    "banking": "Normal banking and shard-aware routing",
    "replication": "PostgreSQL primary and synchronous standby",
    "1": "Application Pod failure",
    "2": "Primary database failure",
    "3": "Concurrent transactions",
    "4": "Coordinator interruption and recovery",
    "5": "Workload-driven autoscaling",
    "6": "Rolling restart, failed update and rollback",
    "7": "Repeated client requests",
    "security": "Enforced network isolation",
}


def show(data):
    print(json.dumps(data, indent=2), flush=True)


@contextmanager
def monitoring_forward(context, service, local_port, remote_port):
    namespace = "gobank-monitoring"
    bank.kubectl(context, namespace, "rollout", "status", f"deployment/{service}",
                 "--timeout=90s", timeout=100)
    address = f"127.0.0.1:{local_port}"
    with TemporaryFile() as output:
        process = subprocess.Popen(
            ["kubectl", "--context", context, "-n", namespace,
             "port-forward", "--address", "127.0.0.1", f"service/{service}",
             f"{local_port}:{remote_port}", "--pod-running-timeout=30s"],
            stdout=output,
        )
        try:
            deadline = time.monotonic() + 35
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    break
                if f"Forwarding from {address} ->".encode() in os.pread(output.fileno(), 4096, 0):
                    yield f"http://{address}"
                    return
                time.sleep(0.1)
            raise RuntimeError(f"Could not forward {service} on {address}. "
                               "Check the monitoring Pods and local port.")
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def deployment():
    print(bank.k("get", "pods,svc,statefulsets,pvc,ingress,hpa,clusters.postgresql.cnpg.io", "-o", "wide"))
    print("CloudNativePG manages the PostgreSQL Pods and PVCs directly.")
    print("Gateway URL", bank.BASE)
    show(bank.ready())


def banking():
    before = bank.snapshot()
    expected = bank.balances(before)
    for account, shard in (("A0500", "a"), ("A0501", "a"), ("A1500", "b")):
        reply = bank.request(f"/accounts/{account}/balance", account=account)
        assert reply["http"] == 200 and reply["balance"] == expected[account], reply
        assert account in before[f"shard-{shard}-db"]["balances"], account
        show({"account": account, "database": f"shard-{shard}-db", "response": reply})
    for action in ("deposit", "withdraw"):
        key = str(uuid.uuid4())
        print(f"{action} request key {key}", flush=True)
        reply = bank.request(f"/accounts/A0500/{action}", {"amount": 5}, "A0500", key)
        assert reply.get("status") == "success", reply
        expected["A0500"] += 5 if action == "deposit" else -5
        assert bank.balances(bank.snapshot()) == expected
        show(reply)
    for destination in ("A0501", "A1500"):
        key = str(uuid.uuid4())
        print(f"Transfer A0500 to {destination}, request key {key}", flush=True)
        reply = bank.transfer("A0500", destination, key=key)
        assert reply["status"] == "success", reply
        expected["A0500"] -= 1
        expected[destination] += 1
        after = bank.wait_for(bank.snapshot)
        assert bank.balances(after) == expected
        show({"response": reply, "database_balances": expected,
              "coordinator_records": [row for row in after["coordinator-db"]["transactions"]
                                      if row["request_key"] == key]})
    assert bank.total(before) == bank.total(after)


def replication():
    show({name: {field: db[field] for field in ("primary", "roles", "replication")}
          for name, db in bank.snapshot().items()})
    print("PostgreSQL recovery roles use f for primary and t for standby.")


def security():
    for target, port, expected in (("shard-a-db-rw", 5432, 0),
                                   ("shard-b-db-rw", 5432, 124),
                                   ("shard-b-headless", 3000, 124),
                                   ("coordinator-db-rw", 5432, 124)):
        # Verify DNS first so a lookup failure cannot pass the isolation check.
        bank.k("exec", "deployment/shard-a", "--", "getent", "hosts", target)
        try:
            bank.k("exec", "deployment/shard-a", "--", "timeout", "3", "bash", "-c",
                   f"echo > /dev/tcp/{target}/{port}")
            status = 0
        except subprocess.CalledProcessError as error:
            status = error.returncode
        print(f"shard-a -> {target}:{port} returned {status}, expected {expected}")
        assert status == expected


def experiment(number, output):
    started = time.monotonic()
    result = bank.run_experiment(number, output)
    before, after = result["before"], result["after"]
    old_ids = {row["id"] for row in before["coordinator-db"]["transactions"]}
    records = [row for row in after["coordinator-db"]["transactions"] if row["id"] not in old_ids]
    observed = result["observed"]
    details = {key: observed[key] for key in ("deleted_pod", "replicas_after", "old_primary",
               "new_primary", "rollout", "key") if key in observed}
    if number == 4:
        for phase in ("interrupted_state", "prepared_after_promotion"):
            db = observed[phase]["shard-a-db"]
            details[phase] = {"primary": db["primary"], "prepared": db["prepared"]}
    if number == 5:
        details["hpa_samples"] = [
            {item["metadata"]["name"]: item["status"] for item in sample["items"]}
            for sample in [observed["initial"], observed["samples"][-1]]
        ]
        details["read_totals"] = {key: sum(worker[key] for worker in observed["read_pressure"])
                                  for key in ("success", "errors")}
    if number == 6:
        print(bank.k("rollout", "history", "deployment/shard-a"))
    show({"seconds": round(time.monotonic() - started, 1), "observed": details,
          "balances_before": bank.balances(before), "balances_after": bank.balances(after),
          "money_before": bank.total(before), "money_after": bank.total(after),
          "new_coordinator_records": len(records), "sample_transactions": records[:3]})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=[*TITLES, "all"])
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", default="gobank")
    parser.add_argument("--no-pause", action="store_true")
    parser.add_argument("--monitoring", action="store_true",
                        help="Open local Grafana and Prometheus port-forwards for this run")
    args = parser.parse_args()
    scenes = ("deployment", "banking", "replication", "1", "2", "4", "3", "5", "6", "7", "security")
    actions = {"deployment": deployment, "banking": banking, "replication": replication, "security": security}
    bank.connect(args.context, args.namespace)
    output = bank.evidence_directory()
    with ExitStack() as forwards:
        if args.monitoring:
            grafana = forwards.enter_context(monitoring_forward(args.context, "grafana", 3001, 3000))
            prometheus = forwards.enter_context(monitoring_forward(args.context, "prometheus", 9091, 9090))
            print(f"Grafana GoBank dashboard {grafana}/d/gobank", flush=True)
            print(f"Prometheus query UI {prometheus}/query", flush=True)
        for index, scene in enumerate(scenes if args.scenario == "all" else [args.scenario]):
            if (index or (args.monitoring and scene == "5")) and not args.no_pause:
                try:
                    input(f"Press Enter for {TITLES[scene]}... ")
                except EOFError:
                    parser.exit(1, "Input closed. Use --no-pause for unattended runs.\n")
            print(f"\n=== {TITLES[scene]} ===", flush=True)
            if scene in actions:
                actions[scene]()
                print(f"PASS {scene}", flush=True)
            else:
                experiment(int(scene), output)


if __name__ == "__main__":
    main()
