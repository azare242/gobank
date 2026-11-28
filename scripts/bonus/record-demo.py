#!/usr/bin/env python3
"""Advance through the five bonus recording scenes by pressing Enter."""

import argparse
import os
from pathlib import Path
import shlex
import subprocess
import sys
from textwrap import dedent


ROOT = Path(__file__).resolve().parents[2]


def pause(title):
    print(f"\n=== {title} ===", flush=True)
    input("Press Enter to show this scene (Ctrl+C to exit) ... ")


def run(command, env):
    print(f"$ {shlex.join(command)}", flush=True)
    subprocess.run(command, cwd=ROOT, env=env, check=True)


def simulate(title, example):
    pause(f"SIMULATION | {title}")
    print("SIMULATION ONLY", flush=True)
    print(dedent(example).strip(), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", default="gobank-bonus")
    args = parser.parse_args()
    env = os.environ.copy()
    env.setdefault("KUBECONFIG", str(ROOT / ".bonus" / "kubeconfig"))
    no_proxy = ",".join(filter(None, (
        env.get("NO_PROXY"), env.get("no_proxy"), "192.168.83.10",
        "192.168.83.11", "192.168.83.12", "127.0.0.1", "localhost",
    )))
    env.update(NO_PROXY=no_proxy, no_proxy=no_proxy)
    kubectl = ["kubectl", "--context", args.context, "-n", "gobank"]

    pause("LIVE | 1. VM and application placement")
    run(["virsh", "-c", "qemu:///system", "list", "--all"], env)
    run([*kubectl, "get", "nodes", "-o", "wide"], env)
    run([sys.executable, "scripts/bonus/demo.py", "topology", "--context", args.context], env)

    pause("LIVE | 2. PostgreSQL replication")
    run([*kubectl, "get", "clusters,pods,pvc", "-o", "wide"], env)
    run([sys.executable, "scripts/bonus/demo.py", "replication", "--context", args.context], env)

    print("\nThe first two scenes were live. The next three are simulated.", flush=True)
    context = shlex.quote(args.context)
    simulate("3. Temporary worker outage", f"""
        Example commands:
          WORKER=gobank-w2  # example; check the live primary placements first
          scripts/bonus/fence-worker.sh "$WORKER"
          scripts/bonus/authorize-promotion.sh {context} "$WORKER"
          kubectl --context {context} -n gobank get nodes,clusters,pods -o wide

        Simulated outcome:
          Worker power-off confirmed before promotion.
          A standby becomes primary. Writes can pause until both standbys return.
          After rejoin, replication is checked and request keys are matched to DB records.
    """)
    simulate("4. Permanent worker loss", f"""
        Example commands after fencing and verifying the independent backup:
          scripts/bonus/destroy-vm.sh "$WORKER"
          # Provision the replacement, join it, and rebuild its local volumes.
          python3 scripts/bonus/demo.py replication --context {context}

        Simulated outcome:
          The replacement worker has a new VM UUID and fresh volumes.
          Both standby streams are restored and retained transfer outcomes are checked.
    """)
    simulate("5. Permanent control-plane loss", f"""
        Example commands after saving object UIDs and verifying the off-VM recovery bundle:
          # Power off the disposable control plane.
          scripts/bonus/destroy-vm.sh gobank-cp
          # Restore etcd and PKI on a replacement VM at the original API endpoint.
          kubectl --context {context} get --raw /readyz
          kubectl --context {context} -n gobank get deployment gateway -o jsonpath='{{.metadata.uid}}'
          kubectl --context {context} -n gobank get pvc

        Simulated outcome:
          The API responds, the original object UIDs match, and a fresh Job completes.
          Database roles and retained transaction outcomes are checked.
    """)
    print("\nWalkthrough complete.")


if __name__ == "__main__":
    try:
        main()
    except (EOFError, KeyboardInterrupt):
        sys.exit("\nWalkthrough stopped.")
    except (OSError, subprocess.CalledProcessError) as error:
        sys.exit(f"\nLive scene failed: {error}")
