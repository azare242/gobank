#!/bin/bash
set -euo pipefail

# Use absolute paths because the calling shell may have a broken PATH.
virsh() { LC_ALL=C /usr/bin/virsh -c qemu:///system "$@"; }

shutdown_guest() {
  local guest=$1 state
  state=$(virsh domstate "$guest")
  if [[ $state == 'shut off' ]]; then
    return
  fi
  if [[ $state != 'in shutdown' ]]; then
    virsh shutdown "$guest"
  fi
  for ((attempt = 0; attempt < 60; attempt++)); do
    state=$(virsh domstate "$guest")
    if [[ $state == 'shut off' ]]; then
      return
    fi
    /usr/bin/sleep 2
  done
  printf '%s did not shut down within two minutes (state: %s).\n' "$guest" "$state" >&2
  return 1
}

shutdown_guest gobank-w1
shutdown_guest gobank-w2
shutdown_guest gobank-cp
virsh list --all
