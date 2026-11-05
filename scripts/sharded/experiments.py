#!/usr/bin/env python3
"""Seven Assignment 2 experiments against a running Compose deployment.

Export the Compose secrets, then run with a scenario number (1..7) or 'all'.
These experiments mutate seeded demo accounts and stop/recreate demo replicas.
They never delete database volumes. Run only against the assignment demo stack.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
BASE = os.environ.get("GATEWAY_URL", "http://127.0.0.1:3000")
TOKENS = {}


def compose(*args, env=None, capture=False):
    return subprocess.run(
        ["docker", "compose", "-f", str(ROOT / "deploy/sharded/compose.yml"), *args],
        env=env, check=True, text=True, capture_output=capture,
    ).stdout


def request(path, body=None, account=None, key=None):
    headers = {"Content-Type": "application/json"}
    if account:
        headers["Authorization"] = "Bearer " + token(account)
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(BASE + path, json.dumps(body).encode() if body is not None else None, headers)
    try:
        response = urllib.request.urlopen(req, timeout=35)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = json.load(response)
        data["http"] = response.status
        data["replica"] = response.headers.get("X-Bank-Replica")
        return data


def token(account):
    if account not in TOKENS:
        reply = request("/login", {"username": account, "password": os.environ["FIXTURE_PASSWORD"]})
        TOKENS[account] = reply["access_token"]
    return TOKENS[account]


def balance(account):
    reply = request(f"/accounts/{account}/balance", account=account)
    assert reply["http"] == 200, reply
    assert reply["balance"] >= 0, reply
    return reply["balance"]


def settle(reply, account):
    deadline = time.monotonic() + 90
    while reply.get("pending"):
        assert time.monotonic() < deadline, reply
        time.sleep(.2)
        reply = request("/transactions/" + reply["transaction_id"], account=account)
    if "outcome" in reply:
        return reply["outcome"]
    return reply


def change(account, amount, action, key=None):
    return settle(request(f"/accounts/{account}/{action}", {"amount": amount}, account, key or str(uuid.uuid4())), account)


def set_balance(account, target):
    current = balance(account)
    if current != target:
        reply = change(account, abs(target-current), "deposit" if current < target else "withdraw")
        assert reply["status"] == "success", reply


def transfer(source, destination, amount, key=None):
    return settle(request("/transfers", {"source": source, "destination": destination, "amount": amount}, source, key or str(uuid.uuid4())), source)


def ready():
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if request("/ready")["http"] == 200:
                return
        except (OSError, ValueError):
            pass
        time.sleep(.5)
    raise RuntimeError("Gateway did not become ready")


def inventory():
    for db in ("db-a", "db-b"):
        out = compose("exec", "-T", db, "psql", "-U", "bank", "-d", "bank", "-Atc",
                      "SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database()", capture=True)
        assert out.strip() == "0", (db, out)


def show_sql(db, query):
    print(f"\nEvidence: {db} — {query}", flush=True)
    compose("exec", "-T", db, "psql", "-U", "bank", "-d", "bank", "-c", query)


def show_prepared():
    for db in ("db-a", "db-b"):
        show_sql(db, "SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database();")


def show_evidence(number):
    print(f"\n--- Experiment {number}: visible evidence ---", flush=True)
    if number == 1:
        for db in ("db-a", "db-b"):
            show_sql(db, "SELECT min(id),max(id),count(*) FROM bank_accounts;")
    elif number in (2, 4):
        compose("ps", "shard-a1", "shard-a2")
        compose("logs", "--tail", "15", "gateway", "shard-a1", "shard-a2")
    elif number == 3:
        show_sql("db-a", "SELECT id,balance FROM bank_accounts WHERE id='A0500';")
    elif number == 5:
        show_sql("db-a", "SELECT id,balance FROM bank_accounts WHERE id IN ('A0500','A0501') ORDER BY id;")
        show_sql("db-b", "SELECT id,balance FROM bank_accounts WHERE id='A1500';")
    elif number in (6, 7):
        show_sql("db-coordinator", "SELECT status,decision,count(*) FROM bank_transactions GROUP BY status,decision ORDER BY status,decision;")
        show_prepared()
        a, b = balance("A0500"), balance("A1500")
        print(f"Gateway balances: A0500={a}, A1500={b}, total={a+b}", flush=True)


def distribution():
    for db, bounds in (("db-a", "A0001|A1000|1000"), ("db-b", "A1001|A2000|1000")):
        result = compose("exec", "-T", db, "psql", "-U", "bank", "-d", "bank", "-Atc",
                         "SELECT min(id),max(id),count(*) FROM bank_accounts", capture=True)
        assert result.strip() == bounds, result
    for account in ("A0001", "A1000", "A1001", "A2000"):
        balance(account)
    assert request("/accounts/A2001/balance", account="A0001")["http"] == 400


def replicas():
    seen = {request("/accounts/A0500/balance", account="A0500")["replica"] for _ in range(30)}
    assert seen == {"a1", "a2"}, seen
    print("Selected replicas:", sorted(seen))


def concurrent_local():
    set_balance("A0500", 1000)
    key = str(uuid.uuid4())
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        replies = list(pool.map(lambda _: change("A0500", 1, "deposit"), range(40)))
        duplicates = list(pool.map(lambda _: change("A0500", 7, "deposit", key), range(20)))
    assert all(r["status"] == "success" for r in replies + duplicates)
    assert balance("A0500") == 1047
    assert change("A0500", 9999, "withdraw")["status"] == "error"
    assert balance("A0500") == 1047


def interruption():
    set_balance("A0500", 1000)
    compose("stop", "shard-a1")
    try:
        time.sleep(2)
        concurrent_local()
        seen = {request("/accounts/A0500/balance", account="A0500")["replica"] for _ in range(10)}
        assert seen == {"a2"}, seen
        print("While A1 is stopped, selected replicas:", sorted(seen), flush=True)
    finally:
        compose("start", "shard-a1")
    time.sleep(2)
    replicas()


def transfers():
    set_balance("A0500", 1000)
    set_balance("A1500", 500)
    assert transfer("A0500", "A1500", 300)["status"] == "success"
    assert (balance("A0500"), balance("A1500")) == (700, 800)
    before = balance("A0501")
    assert transfer("A0500", "A0501", 100)["status"] == "success"
    assert balance("A0501") == before + 100
    assert transfer("A0500", "A0501", 99999)["status"] == "error"
    assert balance("A0500") == 600 and balance("A0501") == before + 100
    inventory()


def recovery():
    for point, expected in (("after_prepare", (1000, 500)), ("all_prepared", (1000, 500)),
                            ("after_decision", (700, 800)), ("after_ack", (700, 800))):
        set_balance("A0500", 1000)
        set_balance("A1500", 500)
        env = dict(os.environ, CRASH_AT=point)
        compose("up", "-d", "--no-deps", "--force-recreate", "gateway", env=env)
        ready()
        key = str(uuid.uuid4())
        try:
            request("/transfers", {"source": "A0500", "destination": "A1500", "amount": 300}, "A0500", key)
        except (OSError, ValueError):
            pass
            print(f"\nCrash checkpoint: {point}; inspect before restart", flush=True)
            compose("ps", "-a", "gateway")
            # The key is generated locally as a UUID, never supplied by a user.
            show_sql("db-coordinator", f"SELECT id,status,decision,acks FROM bank_transactions WHERE request_key='{key}';")
            show_prepared()
        finally:
            compose("up", "-d", "--no-deps", "--force-recreate", "gateway", env=dict(os.environ, CRASH_AT=""))
        ready()
        outcome = transfer("A0500", "A1500", 300, key)
        assert (balance("A0500"), balance("A1500")) == expected, (point, outcome)
        inventory()
        print(f"Recovered {point}: balances={expected}; retry kept the original effect", flush=True)
    assert transfer("A0500", "A1500", 999999)["status"] == "error"
    inventory()


def concurrent_transfers():
    set_balance("A0500", 1000)
    set_balance("A1500", 500)
    jobs = [("A0500", "A1500", str(uuid.uuid4())) if i % 2 == 0
            else ("A1500", "A0500", str(uuid.uuid4())) for i in range(40)]
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        futures = [pool.submit(transfer, j[0], j[1], 1, j[2]) for j in jobs]
        compose("stop", "shard-a1")
        try:
            outcomes = [future.result() for future in futures]
            duplicates = list(pool.map(lambda j: transfer(j[0], j[1], 1, j[2]), jobs))
        finally:
            compose("start", "shard-a1")
    semantic = lambda r: (r.get("status"), r.get("transaction_id"), r.get("decision"), r.get("error"))
    assert list(map(semantic, outcomes)) == list(map(semantic, duplicates)), "replayed results changed"
    assert all(r["status"] in ("success", "error") for r in outcomes)
    assert balance("A0500") + balance("A1500") == 1500
    inventory()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=[str(i) for i in range(1, 8)] + ["all"])
    parser.add_argument("--no-pause", action="store_true",
                        help="Run all experiments without waiting for Enter (automation).")
    args = parser.parse_args()
    scenarios = [distribution, replicas, concurrent_local, interruption, transfers, recovery, concurrent_transfers]
    ready()
    for number, scenario in enumerate(scenarios, 1):
        if args.scenario in (str(number), "all"):
            if args.scenario == "all" and number > 1 and not args.no_pause:
                try:
                    input(f"\nPress Enter to continue to experiment {number} ({scenario.__name__})... ")
                except EOFError:
                    parser.exit(1, "\nInput closed; stopped before the next experiment. Use --no-pause for automation.\n")
            scenario()
            show_evidence(number)
            print(f"PASS {number}: {scenario.__name__}", flush=True)


if __name__ == "__main__":
    main()
