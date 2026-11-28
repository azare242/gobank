#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 1 && $1 =~ ^gobank-w[12]$ ]] || { echo "Usage: $0 gobank-w1|gobank-w2" >&2; exit 2; }
source "$(dirname "$0")/vm-inventory.sh"
read_inventory "$1"
v autostart "$uuid" --disable
if ! assert_off; then
  v destroy "$uuid"
fi
assert_off
printf '%s %s %s shut off\n' "$(date -u +%FT%TZ)" "$domain" "$uuid"
