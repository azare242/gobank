#!/usr/bin/env bash
# Narrow deletion helper for guests built by provision.sh.
set -euo pipefail
[[ $# -eq 1 ]] || { echo "Usage: $0 DOMAIN" >&2; exit 2; }
source "$(dirname "$0")/vm-inventory.sh"
read_inventory "$1"
assert_off
[[ $(realpath -e "$store") == "$store" ]]
[[ -z $(v snapshot-list "$uuid" --name) && -z $(v checkpoint-list "$uuid" --name) ]]
blocks=$(v domblklist "$uuid" --inactive --details)
diff -u <(printf '%s\n' "$store/os.qcow2" "$store/seed.iso" | sort) \
  <(awk 'NR > 2 && NF { print $4 }' <<<"$blocks" | sort)
for file in "$store/os.qcow2" "$store/seed.iso"; do
  [[ $(realpath -e "$file") == "$file" && -f $file ]]
done
sudo qemu-img info --output=json "$store/os.qcow2" | python3 -c '
import json, sys
disk = json.load(sys.stdin)
sys.exit(bool(disk.get("backing-filename") or disk.get("snapshots")
              or disk.get("format-specific", {}).get("data", {}).get("data-file")))
'
printf 'Permanently remove %s (%s) and these files?\n%s\n' "$domain" "$uuid" "$blocks"
read -r -p "Type the domain UUID to confirm: " confirmation
[[ $confirmation == "$uuid" ]]
assert_off
v undefine "$uuid"
sudo rm -- "$store/os.qcow2" "$store/seed.iso"
sudo rmdir -- "$store"
mv -- "$root/.bonus/$domain.inventory" "$root/.bonus/$domain-$uuid.removed"
printf '%s %s %s removed\n' "$(date -u +%FT%TZ)" "$domain" "$uuid"
