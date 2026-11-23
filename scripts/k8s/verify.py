#!/usr/bin/env python3
"""Read database evidence from a running Assignment 3 cluster (no mutations)."""
import argparse
import json
import subprocess


def kubectl(context, namespace, *args, timeout=45):
    return subprocess.check_output(
        ["kubectl", "--context", context, "-n", namespace, *args], text=True,
        timeout=timeout,
    ).strip()


def inventory(context, namespace):
    result = {}
    for name in ("shard-a-db", "shard-b-db", "coordinator-db"):
        cluster = json.loads(kubectl(context, namespace, "get", "cluster", name, "-o", "json"))
        primary = cluster["status"]["currentPrimary"]

        def sql(query):
            raw = kubectl(context, namespace, "exec", primary, "-c", "postgres", "--",
                          "psql", "-X", "-U", "postgres", "-d", "bank", "-Atc", query)
            return json.loads(raw)

        data = {
            "primary": primary,
            "replication": sql("SELECT coalesce(json_agg(t),'[]') FROM "
                               "(SELECT application_name,state,sync_state FROM pg_stat_replication) t"),
            "prepared": sql("SELECT coalesce(json_agg(t),'[]') FROM "
                            "(SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database()) t"),
        }
        if name == "coordinator-db":
            data["transactions"] = sql("SELECT coalesce(json_agg(t),'[]') FROM "
                                       "(SELECT id,request_key,status,decision,acks,participants FROM bank_transactions ORDER BY id) t")
        else:
            data["balances"] = sql("SELECT json_object_agg(id,balance) FROM bank_accounts "
                                   "WHERE id IN ('A0500','A0501','A1500')")
            data["total"] = sql("SELECT coalesce(sum(balance),0) FROM bank_accounts")
            data["operations"] = sql("SELECT count(*) FROM bank_operations")
            data["participants"] = sql("SELECT coalesce(json_agg(t),'[]') FROM "
                                       "(SELECT id,state FROM bank_participants ORDER BY id) t")
        pods = json.loads(kubectl(context, namespace, "get", "pods", "-l",
                                 "cnpg.io/cluster=" + name, "-o", "json"))
        data["roles"] = {}
        for pod in pods["items"]:
            if not any(c["name"] == "postgres" for c in pod["spec"]["containers"]):
                continue  # CNPG bootstrap and join Jobs are not DB instances.
            pod_name = pod["metadata"]["name"]
            data["roles"][pod_name] = kubectl(
                context, namespace, "exec", pod_name, "-c", "postgres", "--",
                "psql", "-X", "-U", "postgres", "-d", "bank", "-Atc",
                "SELECT pg_is_in_recovery()")
        result[name] = data
    return result


def verify(data):
    for name, cluster in data.items():
        assert list(cluster["roles"].values()).count("f") == 1, (name, cluster["roles"])
        assert any(r["state"] == "streaming" and r["sync_state"] in ("sync", "quorum")
                   for r in cluster["replication"]), (name, "no synchronous standby")
        assert not cluster["prepared"], (name, "unresolved prepared transactions")
    assert all(t["status"] == "COMPLETED" for t in data["coordinator-db"]["transactions"]), "pending coordinator records"
    for transaction in data["coordinator-db"]["transactions"]:
        if len(transaction["participants"]) < 2:
            continue  # Same-shard mutations use bank_operations, not 2PC rows.
        for member in transaction["participants"]:
            shard = member["shard_id"]
            states = {p["id"]: p["state"] for p in data[f"shard-{shard}-db"]["participants"]}
            assert states.get(transaction["id"]) == transaction["decision"], (transaction["id"], shard, "participant decision mismatch")
            assert shard in transaction["acks"], (transaction["id"], "missing acknowledgment")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", default="gobank-assignment3")
    parser.add_argument("--namespace", default="gobank")
    args = parser.parse_args()
    data = inventory(args.context, args.namespace)
    print(json.dumps(data, indent=2))
    verify(data)


if __name__ == "__main__":
    main()
