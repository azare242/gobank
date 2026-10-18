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
nohup ./bin/cluster-server -config "deploy/cluster/$node.json" >>"logs/$node.log" 2>&1 &
pid=$!
echo "$pid" >"logs/$node.pid"
sleep 1
if ! kill -0 "$pid" 2>/dev/null; then tail -n 20 "logs/$node.log"; exit 1; fi
echo "Node $node started, PID $pid. Logs: logs/$node.log"
