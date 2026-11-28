#!/usr/bin/env python3
"""Print prebound local PVs. Arguments can describe fresh replacement claims."""
import argparse
import json
import re

PLACEMENT = {
    "shard-a-db": ["gobank-w1", "gobank-w1", "gobank-w2"],
    "shard-b-db": ["gobank-w2", "gobank-w2", "gobank-w1"],
    "coordinator-db": ["gobank-w1", "gobank-w1", "gobank-w2"],
}


def volume(claim, node):
    return {
        "apiVersion": "v1",
        "kind": "PersistentVolume",
        "metadata": {"name": f"gobank-local-{claim}", "labels": {"gobank.io/storage": "local"}},
        "spec": {
            "capacity": {"storage": "2Gi"},
            "accessModes": ["ReadWriteOnce"],
            "storageClassName": "gobank-local",
            "persistentVolumeReclaimPolicy": "Retain",
            "claimRef": {"namespace": "gobank", "name": claim},
            "local": {"path": f"/var/lib/gobank/postgres/{claim}"},
            "nodeAffinity": {"required": {"nodeSelectorTerms": [{"matchExpressions": [
                {"key": "kubernetes.io/hostname", "operator": "In", "values": [node]}
            ]}]}},
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("placements", nargs="*", metavar="CLAIM=NODE")
    args = parser.parse_args()
    placements = {}
    for entry in args.placements:
        claim, _, node = entry.partition("=")
        if (not re.fullmatch(r"(shard-[ab]-db|coordinator-db)-[1-9][0-9]*", claim)
                or node not in ("gobank-w1", "gobank-w2") or claim in placements):
            parser.error(f"invalid or duplicate placement: {entry}")
        placements[claim] = node
    if not placements:
        for cluster, nodes in PLACEMENT.items():
            for index, node in enumerate(nodes, 1):
                placements[f"{cluster}-{index}"] = node
    print(json.dumps({"apiVersion": "v1", "kind": "List",
                      "items": [volume(claim, node) for claim, node in placements.items()]}, indent=2))


if __name__ == "__main__":
    main()
