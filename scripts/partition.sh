#!/usr/bin/env bash
# Run on the leader VM. Blocks only Raft traffic; HTTP and SSH stay reachable.
set -euo pipefail
mode=${1:-}
shift || true
case "$mode" in on|off) ;; *) echo "Usage: sudo bash scripts/partition.sh on|off [peer-ip peer-ip]" >&2; exit 2;; esac
if [[ $EUID -ne 0 ]]; then echo "Run through sudo" >&2; exit 2; fi
if [[ $# -eq 0 ]]; then set -- 192.168.56.12 192.168.56.13; fi
chain=BANK_LAB_PARTITION
if [[ $mode == off ]]; then
  for direction in INPUT OUTPUT; do
    while iptables -C "$direction" -j "$chain" 2>/dev/null; do iptables -D "$direction" -j "$chain"; done
  done
  if iptables -L "$chain" -n >/dev/null 2>&1; then iptables -F "$chain"; iptables -X "$chain"; fi
  echo 'Raft connectivity restored; other firewall rules were preserved.'
  exit 0
fi
if iptables -L "$chain" -n >/dev/null 2>&1; then echo 'Lab partition already configured; run off before changing peers.' >&2; exit 2; fi
for peer in "$@"; do
  if [[ ! $peer =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then echo "Invalid peer IPv4: $peer" >&2; exit 2; fi
done
iptables -N "$chain"
for peer in "$@"; do
  for port_flag in --sport --dport; do
    iptables -A "$chain" -s "$peer" -p tcp "$port_flag" 7000 -j DROP
    iptables -A "$chain" -d "$peer" -p tcp "$port_flag" 7000 -j DROP
  done
done
# Insert ahead of established-connection ACCEPT rules so existing streams break.
iptables -I INPUT 1 -j "$chain"
iptables -I OUTPUT 1 -j "$chain"
iptables -L "$chain" -n -v
echo 'Partition active. Keep all server processes running. Restore with: sudo bash scripts/partition.sh off'
