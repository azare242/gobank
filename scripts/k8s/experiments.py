#!/usr/bin/env python3
"""Run Assignment 3 experiments against an isolated Minikube demo deployment.

Mutates fixture accounts and restarts demo workloads. Never deletes PVCs or
edits balances/decisions with SQL. Use --context to explicitly choose a cluster.
Point GATEWAY_URL at the Ingress, NodePort, or a persistent port-forward.
"""
import argparse
import base64
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid

from verify import inventory, kubectl, verify

ROOT = Path(__file__).resolve().parents[2]
BASE = os.environ.get("GATEWAY_URL", "http://127.0.0.1:3000")
TOKENS = {}
CONTEXT = ""
NAMESPACE = "gobank"


def k(*args):
    return kubectl(CONTEXT, NAMESPACE, *args)


def wait_for(check, timeout=300):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (AssertionError, OSError, ValueError, subprocess.SubprocessError) as error:
            last = error
        time.sleep(2)
    raise RuntimeError(f"Timed out waiting for condition: {last}")


def request(path, body=None, account=None, key=None):
    headers = {"Content-Type": "application/json", "Host": "gobank.local"}
    if account:
        headers["Authorization"] = "Bearer " + TOKENS[account]
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(BASE + path, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        response = urllib.request.urlopen(req, timeout=40)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = json.load(response)
        data["http"] = response.status
        data["replica"] = response.headers.get("X-Bank-Replica")
        return data


def ready():
    return wait_for(lambda: request("/ready")["http"] == 200)


def transfer(source, destination, key=None, amount=1):
    key = key or str(uuid.uuid4())
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            reply = request("/transfers", {"source": source, "destination": destination,
                                          "amount": amount}, source, key)
            while reply.get("pending") and time.monotonic() < deadline:
                time.sleep(.2)
                reply = request("/transactions/" + reply["transaction_id"], account=source)
            if "outcome" in reply:
                reply = reply["outcome"]
            if reply.get("status") in ("success", "error") and reply.get("http", 200) < 500:
                return reply
        except (OSError, ValueError):
            pass
        time.sleep(.3)
    raise RuntimeError("Unresolved transfer; retain original key: " + key)


def snapshot():
    data = inventory(CONTEXT, NAMESPACE)
    verify(data)
    return data


def balances(data):
    return {key: value for name in ("shard-a-db", "shard-b-db")
            for key, value in data[name]["balances"].items()}


def total(data):
    return sum(data[name]["total"] for name in ("shard-a-db", "shard-b-db"))


def rollout(name):
    kubectl(CONTEXT, NAMESPACE, "rollout", "status", "deployment/" + name,
            "--timeout=180s", timeout=195)
    return True


def concurrent_load(count=40):
    jobs = [("A0500", "A1500") if i % 2 == 0 else ("A1500", "A0500") for i in range(count)]
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        replies = list(pool.map(lambda pair: transfer(*pair), jobs))
    assert all(r.get("status") in ("success", "error") for r in replies), replies
    delta = sum((-1 if source == "A0500" else 1) for (source, _), reply in zip(jobs, replies)
                if reply.get("status") == "success")
    # Completed outcomes determine the exact expected balances, including aborts.
    return {"replies": replies, "source_delta": delta}


def app_failure():
    pods = json.loads(k("get", "pods", "-l", "app.kubernetes.io/name=shard-a", "-o", "json"))["items"]
    victim = pods[0]["metadata"]["name"]
    k("delete", "pod", victim, "--grace-period=0", "--force", "--wait=false")
    replies = concurrent_load()
    wait_for(lambda: rollout("shard-a"))
    ready()
    seen = {request("/accounts/A0500/balance", account="A0500")["replica"] for _ in range(30)}
    assert victim not in seen and len(seen) >= 2, seen
    return {"deleted_pod": victim, "replicas_after": sorted(seen), "requests": replies}


def db_failure():
    victim = json.loads(k("get", "cluster", "shard-a-db", "-o", "json"))["status"]["currentPrimary"]
    k("delete", "pod", victim, "--grace-period=0", "--force", "--wait=false")
    promoted = wait_for(lambda: (lambda p: p if p != victim else None)(
        json.loads(k("get", "cluster", "shard-a-db", "-o", "json"))["status"]["currentPrimary"]))
    ready()
    replies = concurrent_load()
    wait_for(snapshot)
    return {"old_primary": victim, "new_primary": promoted, "requests": replies}


def coordinator_failure():
    original = json.loads(k("get", "deployment", "gateway", "-o", "json"))["spec"]["template"]["spec"]["containers"][0]
    def command(value):
        k("patch", "deployment", "gateway", "--type=strategic", "-p", json.dumps({
            "spec": {"template": {"spec": {"containers": [{"name": "gateway", "command": value}]}}}}))
    saved_hpa = json.loads(k("get", "hpa", "gateway", "-o", "json"))
    saved_hpa.pop("status", None)
    for field in ("resourceVersion", "uid", "creationTimestamp", "managedFields"):
        saved_hpa["metadata"].pop(field, None)
    k("delete", "hpa", "gateway")
    k("scale", "deployment/gateway", "--replicas=1")
    try:
        wait_for(lambda: rollout("gateway"))
        # Hold the container after the injected process exit so automatic restart
        # cannot recover the transaction before its durable boundary is captured.
        command(["/bin/sh", "-c", 'gateway; result=$?; echo "gateway exited: $result"; if [ "$result" -eq 86 ]; then sleep 120; fi; exit "$result"'])
        k("set", "env", "deployment/gateway", "CRASH_AT=after_decision")
        wait_for(lambda: rollout("gateway"))
        def only_injected_gateway():
            pods = json.loads(k("get", "pods", "-l", "app.kubernetes.io/name=gateway", "-o", "json"))["items"]
            return len(pods) == 1 and not pods[0]["metadata"].get("deletionTimestamp")
        wait_for(only_injected_gateway)
        ready()
        key = str(uuid.uuid4())
        try:
            request("/transfers", {"source": "A0500", "destination": "A1500", "amount": 1}, "A0500", key)
        except (OSError, ValueError):
            pass
        # Stop automatic restarts while inspecting the durable crash boundary.
        k("scale", "deployment/gateway", "--replicas=0")
        wait_for(lambda: not json.loads(k("get", "pods", "-l", "app.kubernetes.io/name=gateway", "-o", "json"))["items"])
        def decision():
            data = inventory(CONTEXT, NAMESPACE)
            rows = [t for t in data["coordinator-db"]["transactions"] if t["request_key"] == key]
            return data if rows and rows[0]["decision"] == "COMMIT" else None
        interrupted = wait_for(decision)
        assert any(c["prepared"] for c in interrupted.values()), "Missed the prepared transaction boundary"
        # Keep the coordinator stopped while promoting a synchronous standby.
        # This proves prepared WAL survives promotion before recovery can resolve it.
        victim = interrupted["shard-a-db"]["primary"]
        gids = {p["gid"] for p in interrupted["shard-a-db"]["prepared"]}
        k("delete", "pod", victim, "--grace-period=0", "--force", "--wait=false")
        def prepared_after_promotion():
            state = inventory(CONTEXT, NAMESPACE)
            if state["shard-a-db"]["primary"] == victim:
                return None
            assert gids and gids <= {p["gid"] for p in state["shard-a-db"]["prepared"]}, "Prepared state lost on promotion"
            return state
        promoted = wait_for(prepared_after_promotion)
        command(original.get("command"))
        k("set", "env", "deployment/gateway", "CRASH_AT-")
        k("scale", "deployment/gateway", "--replicas=1")
        wait_for(lambda: rollout("gateway"))
        ready()
        reply = transfer("A0500", "A1500", key)
        assert reply["status"] == "success", reply
        return {"key": key, "interrupted_state": interrupted, "prepared_after_promotion": promoted,
                "replayed_outcome": reply}
    finally:
        command(original.get("command"))
        k("set", "env", "deployment/gateway", "CRASH_AT-")
        subprocess.run(["kubectl", "--context", CONTEXT, "-n", NAMESPACE, "apply", "-f", "-"],
                       input=json.dumps(saved_hpa), text=True, check=True)
        k("scale", "deployment/gateway", "--replicas=2")
        wait_for(lambda: rollout("gateway"))


def autoscaling():
    before = json.loads(k("get", "hpa", "-o", "json"))
    initial = {h["metadata"]["name"]: h["status"].get("currentReplicas", 2) for h in before["items"]}
    deadline = time.monotonic() + 360
    observed = []
    batches = []
    scaled = False
    stop = threading.Event()
    def reads():
        count = errors = 0
        while not stop.is_set():
            try:
                reply = request("/accounts/A0500/balance", account="A0500")
                count += reply["http"] == 200
                errors += reply["http"] != 200
            except (OSError, ValueError):
                errors += 1
        return {"success": count, "errors": errors}
    with concurrent.futures.ThreadPoolExecutor(max_workers=65) as pool:
        readers = [pool.submit(reads) for _ in range(64)]
        try:
            while time.monotonic() < deadline:
                load = pool.submit(concurrent_load, 120)
                hpas = json.loads(k("get", "hpa", "-o", "json"))
                observed.append(hpas)
                scaled = scaled or any(h["status"].get("currentReplicas", 0) > initial[h["metadata"]["name"]]
                                       for h in hpas["items"])
                batches.append(load.result())
                if scaled:
                    break
        finally:
            stop.set()
        read_results = [reader.result() for reader in readers]
    assert scaled, "No workload-driven HPA scale-up observed within 6 minutes"
    return {"initial": before, "samples": observed, "read_pressure": read_results, "requests": batches}


def updates():
    deployment = json.loads(k("get", "deployment", "shard-a", "-o", "json"))
    image = deployment["spec"]["template"]["spec"]["containers"][0]["image"]
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        load = pool.submit(concurrent_load, 160)
        k("rollout", "restart", "deployment/shard-a")
        wait_for(lambda: rollout("shard-a"))
        first = load.result()
        load = pool.submit(concurrent_load, 160)
        try:
            k("set", "image", "deployment/shard-a", "shard=" + image + "-deliberately-invalid")
            wait_for(lambda: any(c["state"].get("waiting", {}).get("reason") in ("ErrImagePull", "ImagePullBackOff")
                                 for p in json.loads(k("get", "pods", "-l", "app.kubernetes.io/name=shard-a", "-o", "json"))["items"]
                                 for c in p.get("status", {}).get("containerStatuses", [])), 120)
            ready()
            failed = k("get", "replicasets", "-l", "app.kubernetes.io/name=shard-a", "-o", "json")
        finally:
            k("rollout", "undo", "deployment/shard-a")
            wait_for(lambda: rollout("shard-a"))
        second = load.result()
    return {"rollout": "same-image rolling restart", "failed_update_replicasets": json.loads(failed),
            "requests": [first, second]}


def repeated():
    key = str(uuid.uuid4())
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        replies = list(pool.map(lambda _: transfer("A0500", "A1500", key), range(20)))
    assert all(r["status"] == "success" for r in replies), replies
    data = snapshot()
    rows = [t for t in data["coordinator-db"]["transactions"] if t["request_key"] == key]
    assert len(rows) == 1 and rows[0]["decision"] == "COMMIT", rows
    return {"key": key, "requests": replies, "transaction": rows[0]}


SCENARIOS = (app_failure, db_failure, concurrent_load, coordinator_failure,
             autoscaling, updates, repeated)


def connect(context, namespace):
    global CONTEXT, NAMESPACE
    CONTEXT, NAMESPACE = context, namespace
    secret = json.loads(k("get", "secret", "gobank-app-secrets", "-o", "json"))
    password = base64.b64decode(secret["data"]["FIXTURE_PASSWORD"]).decode()
    ready()
    for account in ("A0500", "A0501", "A1500"):
        TOKENS[account] = request("/login", {"username": account, "password": password})["access_token"]


def evidence_directory():
    output = ROOT / "scripts/k8s/evidence" / datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    output.mkdir(parents=True)
    return output


def run_experiment(number, output):
    scenario = SCENARIOS[number - 1]
    result = {"experiment": number, "injection": scenario.__name__, "passed": False}
    print(f"Running {number}: {scenario.__name__}", flush=True)
    try:
        result["before"] = inventory(CONTEXT, NAMESPACE)
        if number != 2:
            verify(result["before"])
        result["observed"] = scenario()
        result["after"] = wait_for(snapshot)
        assert total(result["before"]) == total(result["after"]), "Money conservation failed"
        observed = result["observed"]
        if number in (4, 7):
            delta = -1
        elif number == 3:
            delta = observed["source_delta"]
        elif number in (1, 2):
            delta = observed["requests"]["source_delta"]
        else:
            delta = sum(batch["source_delta"] for batch in observed["requests"])
        expected = balances(result["before"])
        expected["A0500"] += delta
        expected["A1500"] -= delta
        actual = balances(result["after"])
        assert actual == expected, (expected, actual)
        result["passed"] = True
    except Exception as error:
        result["error"] = str(error)
        raise
    finally:
        (output / f"experiment-{number}.json").write_text(json.dumps(result, indent=2) + "\n")
        print(f"Evidence: {output / f'experiment-{number}.json'}", flush=True)
    print(f"PASS {number}", flush=True)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=[str(i) for i in range(1, 8)] + ["all"])
    parser.add_argument("--context", default="gobank-assignment3")
    parser.add_argument("--namespace", default="gobank")
    args = parser.parse_args()
    connect(args.context, args.namespace)
    output = evidence_directory()
    for number in range(1, 8):
        if args.scenario in (str(number), "all"):
            run_experiment(number, output)


if __name__ == "__main__":
    main()
