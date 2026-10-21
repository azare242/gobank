#!/usr/bin/env bash
# Client-only demo. Servers must already be running.
set -Eeuo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
export BANK_SERVERS="${BANK_SERVERS:-http://192.168.56.11:3000,http://192.168.56.12:3000,http://192.168.56.13:3000}"
IFS=, read -ra urls <<< "$BANK_SERVERS"
run=$(cat /proc/sys/kernel/random/uuid)
mkdir -p bin
client="$PWD/bin/bank-client"
journal_dir="$PWD/client-journals/$run"
mkdir -p "$journal_dir"
username="demo-${run:0:12}"
export BANK_PASSWORD="demo-$run"
trap 'echo "ERROR: Demo stopped." >&2' ERR
step() { printf '\n%s\n' "$1"; read -r -p 'Press ENTER to continue: '; }
bank() { "$client" -servers "$BANK_SERVERS" "$@"; }
login() {
  local reply
  reply=$(bank -action login -username "$username")
  BANK_TOKEN=$(jq -er .access_token <<< "$reply")
  BANK_ACCOUNT=$(jq -er .account <<< "$reply")
  export BANK_TOKEN BANK_ACCOUNT
  echo 'SUCCESS: Login.'
}
status() {
  local url body failed=0
  for url in "${urls[@]}"; do
    if body=$(curl --noproxy '*' -fsS --max-time 3 "$url/cluster/status" 2>/dev/null); then
      jq -r '"SUCCESS: Node \(.node), role \(.role), digest \(.state_digest)."' <<< "$body"
    else
      echo "ERROR: $url unavailable."
      failed=1
    fi
  done
  return 0
}
prepare() {
  login
  local url body leader='' deadline=$((SECONDS+30))
  for url in "${urls[@]}"; do
    if body=$(curl --noproxy '*' -fsS --max-time 3 "$url/cluster/status" 2>/dev/null) &&
       jq -e '.role=="Leader"' <<< "$body" >/dev/null; then leader=$url; break; fi
  done
  [[ -n $leader ]] || { echo 'ERROR: No leader available.'; return 1; }
  if [[ $leader != "${urls[0]}" ]]; then
    curl --noproxy '*' -fsS --max-time 15 -X POST "$leader/cluster/transfer/a" -H "Authorization: Bearer $BANK_TOKEN" >/dev/null
  fi
  while (( SECONDS < deadline )); do
    if curl --noproxy '*' -fsS --max-time 3 "${urls[0]}/cluster/status" | jq -e '.role=="Leader"' >/dev/null; then
      echo 'SUCCESS: Initial leader ready.'; return
    fi
    sleep 1
  done
  echo 'ERROR: Initial leader preparation failed.'; return 1
}
workload() {
  local name=$1 reply
  local count=1200 workers=4 interval=200ms timeout=45s
  if [[ $name == concurrent ]]; then
    count=100
    workers=10
    interval=0s
    timeout=10s
  fi
  prepare
  case "$name" in
    concurrent) echo 'Keep the leader and all followers running. Do not kill any process during concurrency.' ;;
    failover)
      echo 'Once successful operations below start printing, kill ONLY the leader process.'
      echo 'Keep all followers running.'
      echo 'Watch requests resume through the newly elected leader.'
      ;;
    partition)
      echo 'Keep ALL processes running: do not kill leader or followers.'
      echo 'Once successful operations below start printing, isolate the initial leader from both followers.'
      echo 'Block only replication traffic; keep client access available.'
      echo 'After requests resume through the new leader, restore connectivity to the isolated server while traffic is active.'
      echo 'Keep every server process running.'
      ;;
  esac
  echo "Starting $name: $count operations, $workers workers, interval $interval, retry limit $timeout per operation."
  if ! bank -action workload -count "$count" -workers "$workers" -interval "$interval" -timeout "$timeout" -amount 1 -journal "$journal_dir/$name.jsonl"; then
    echo 'ERROR: Workload has unresolved operations.'
  fi
  echo "Finished submitting $name workload."
  if [[ $name == failover ]]; then
    step 'Restart the stopped former leader. Keep all followers running. Press ENTER after it has restarted.'
  elif [[ $name == partition ]]; then
    step 'Ensure connectivity to the isolated former leader is restored. Keep all processes running, then press ENTER.'
  fi
  step "Verify $name accounting"
  login
  reply=$(bank -action reconcile -journal "$journal_dir/$name.jsonl")
  printf '%s\n' "$reply"
  tail -n 1 <<< "$reply" | jq -e '.verified==true' >/dev/null
  echo "SUCCESS: $name accounting verified."
}
step 'Prepare client'
for command in curl jq go; do command -v "$command" >/dev/null; done
go build -o "$client" ./cmd/client
echo 'SUCCESS: Client ready.'
step 'Cluster health and roles'
for url in "${urls[@]}"; do
  curl --noproxy '*' -fsS --max-time 5 "$url/health" | jq -e '.status=="ok"' >/dev/null
  echo "SUCCESS: $url healthy."
done
status
step 'Register and log in'
bank -action register -username "$username" -id "signup-$run"
login
step 'Deposit, withdraw and balance'
bank -action deposit -amount 1000 -journal "$journal_dir/setup.jsonl"
bank -action withdraw -amount 100 -journal "$journal_dir/setup.jsonl"
bank -action balance
echo 'SUCCESS: Basic operations completed.'
step 'Core requests, errors and rollback'
prepare
bash scripts/core-demo.sh "${urls[0]}"
step 'Concurrent requests'
workload concurrent
step 'Automatic failover demo: start live client traffic'
workload failover
step 'Network partition demo: start live client traffic'
workload partition
step 'Final cluster convergence'
deadline=$((SECONDS+60))
while :; do
  rows='[]'
  for url in "${urls[@]}"; do
    if body=$(curl --noproxy '*' -fsS --max-time 3 "$url/cluster/status" 2>/dev/null); then
      rows=$(jq --argjson row "$body" '. + [$row]' <<< "$rows")
    fi
  done
  if jq -e 'length==3 and ([.[]|select(.role=="Leader")]|length)==1 and
    ([.[]|select(.role=="Follower")]|length)==2 and
    ([.[].state_digest]|unique|length)==1 and (.[0].state_digest|type)=="string" and
    (.[0].state_digest|length)>0' <<< "$rows" >/dev/null; then break; fi
  if (( SECONDS >= deadline )); then echo 'ERROR: Cluster did not converge.'; exit 1; fi
  sleep 2
done
status
echo 'SUCCESS: Final cluster convergence verified.'
echo 'SUCCESS: Client demo complete.'
