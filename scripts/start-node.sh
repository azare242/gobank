#!/usr/bin/env bash
set -euo pipefail
node=${1:-}
case "$node" in a|b|c) ;; *) echo 'Usage: bash scripts/start-node.sh a|b|c' >&2; exit 2;; esac
cd "$(dirname "$0")/.."
set -a
source ./.env
set +a
mkdir -p logs
if [[ -f logs/$node.pid ]] && kill -0 "$(cat "logs/$node.pid")" 2>/dev/null; then
  echo "PID in logs/$node.pid is still running; inspect it before starting another node." >&2
  exit 1
fi
case "$node" in
  a) lab_address=192.168.56.11/24 ;;
  b) lab_address=192.168.56.12/24 ;;
  c) lab_address=192.168.56.13/24 ;;
esac
if ! command -v nmcli >/dev/null 2>&1; then
  echo 'NetworkManager (nmcli) is required to configure enp0s8.' >&2
  exit 1
fi
if nmcli connection show bank-lab >/dev/null 2>&1; then
  sudo nmcli connection modify bank-lab connection.interface-name enp0s8 \
    ipv4.method manual ipv4.addresses "$lab_address" \
    ipv4.gateway '' ipv4.never-default yes connection.autoconnect yes
else
  sudo nmcli connection add type ethernet \
    con-name bank-lab ifname enp0s8 \
    ipv4.method manual ipv4.addresses "$lab_address" \
    ipv4.never-default yes connection.autoconnect yes
fi
sudo nmcli connection up bank-lab
nohup ./bin/cluster-server -config "deploy/cluster/$node.json" >>"logs/$node.log" 2>&1 &
pid=$!
echo "$pid" >"logs/$node.pid"
sleep 1
if ! kill -0 "$pid" 2>/dev/null; then tail -n 20 "logs/$node.log"; exit 1; fi
echo "Node $node started, PID $pid. Logs: logs/$node.log"
