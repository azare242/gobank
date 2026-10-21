#!/usr/bin/env bash
# Run on the client VM after exporting BANK_TOKEN and BANK_ACCOUNT.
# Give the CURRENT LEADER URL as argument. Use a separate demo account when
# a workload is running, otherwise invariant verification will rightly fail.
set -euo pipefail
base=${1:?Usage: bash scripts/core-demo.sh http://CURRENT-LEADER:3000}
: "${BANK_TOKEN:?Set BANK_TOKEN}"
: "${BANK_ACCOUNT:?Set BANK_ACCOUNT}"
call() {
  local label=$1 method=$2 route=$3 body=${4:-} fault=${5:-false}
  local op="demo-$(cat /proc/sys/kernel/random/uuid)"
  local -a data_args=()
  if [[ -n $body ]]; then data_args=(--data "$body"); fi
  printf '\n%s\n' "$label"
  curl --max-time 10 -sS -X "$method" "$base$route" \
    -H "Authorization: Bearer $BANK_TOKEN" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $op" -H "X-Demo-Fail-Before-Commit: $fault" \
    "${data_args[@]}" | jq .
}
call 'Balance' GET "/accounts/$BANK_ACCOUNT/balance"
call 'Deposit 500' POST "/accounts/$BANK_ACCOUNT/deposit" '{"amount":500}'
call 'Withdraw 100' POST "/accounts/$BANK_ACCOUNT/withdraw" '{"amount":100}'
call 'Unknown account' POST /accounts/Z9999/deposit '{"amount":1}'
call 'Insufficient funds' POST "/accounts/$BANK_ACCOUNT/withdraw" '{"amount":9223372036854775807}'
call 'Negative amount' POST "/accounts/$BANK_ACCOUNT/deposit" '{"amount":-50}'
call 'Zero amount' POST "/accounts/$BANK_ACCOUNT/deposit" '{"amount":0}'
call 'Non-numeric amount' POST "/accounts/$BANK_ACCOUNT/deposit" '{"amount":"abc"}'
call 'Missing amount' POST "/accounts/$BANK_ACCOUNT/withdraw" '{}'
call 'Malformed JSON' POST "/accounts/$BANK_ACCOUNT/withdraw" '{broken'
call 'Unsupported action' POST "/accounts/$BANK_ACCOUNT/transfer" '{"amount":1}'
call 'Before rollback demo' GET "/accounts/$BANK_ACCOUNT/balance"
call 'Tentative SQL write then rollback' POST "/accounts/$BANK_ACCOUNT/deposit" '{"amount":123}' true
call 'After rollback: balance must be unchanged' GET "/accounts/$BANK_ACCOUNT/balance"
