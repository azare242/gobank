# Assignment 3 bonus demo

Run from the repository root once the three libvirt VMs are provisioned, kubeadm
and CloudNativePG are installed, and the multi-VM overlay and rendered local
volumes are deployed. Use the kubeconfig context for that cluster.

```bash
scripts/bonus/start-demo.sh YOUR_CONTEXT
```

Add `--start-vms` to start the inventoried VMs if they are shut off. The script
prints VM identities and disks, waits for the nodes and deployments, then runs
the following checks:

1. Show the control plane and two workers, with ready Gateway, Shard A, and
   Shard B replicas on both workers.
2. Show the primary and both standbys for each shard and the coordinator,
   their worker-local volumes, and the effective two-standby synchronous policy.
   PostgreSQL reports both replication streams and their flush and replay
   positions. The checks require exactly one writable instance per database.

For a cluster that is already ready, run `python3 scripts/bonus/demo.py --context
YOUR_CONTEXT`. Pass `topology` or `replication` to run an individual check.
Results are stored under `.bonus/evidence/`. The demo only reads infrastructure
and PostgreSQL replication state. It needs no test accounts or transfers.

## Recording walkthrough

Run from the repository root. Press Enter once for each scene and use the
[Persian transcript](../../docs/kubernetes-bonus-demo-fa.pdf) for narration.

```bash
export KUBECONFIG="$PWD/.bonus/kubeconfig"
scripts/bonus/start-demo.sh gobank-bonus --start-vms
python3 scripts/bonus/record-demo.py
```

Scenes 1–2 run the live checks. Scenes 3–5 print labeled example commands and
simulated outcomes. For those scenes, use the simulation introduction printed
by the script and describe the examples as hypothetical.

## Assignment evidence still needed

The walkthrough covers the topology and replication portion of Bonus I.
Independent backups, worker interruption under load, permanent worker loss and
redundancy rebuild, and control-plane restoration with new Pod scheduling all
need separate live evidence. The PDF calls for transaction-state verification
during the failure scenarios. The existing fence, promotion authorization, and
VM deletion helpers cover individual operations. The recovery procedures are
outlined in the [bonus plan](../../assignment3-kubernetes-bonuses-plan.md).
