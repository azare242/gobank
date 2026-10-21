#!/usr/bin/env bash
set -euo pipefail
servers=${BANK_SERVERS:-http://192.168.56.11:3000,http://192.168.56.12:3000,http://192.168.56.13:3000}
IFS=',' read -r -a urls <<< "$servers"
for url in "${urls[@]}"; do
  printf '%s ' "$url"
  if body=$(curl --max-time 3 -fsS "$url/cluster/status" 2>/dev/null); then
    jq -c '{node,role,leader,applied_index,state_digest,term:.raft.term}' <<< "$body"
  else
    echo 'unreachable'
  fi
done
