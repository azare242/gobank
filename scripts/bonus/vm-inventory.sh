# Shared checks for host-side libvirt operations.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
v() { LC_ALL=C virsh -c qemu:///system "$@"; }
read_inventory() {
  domain=$1
  case "$domain" in gobank-cp|gobank-w1|gobank-w2) ;; *) return 2 ;; esac
  [[ -r "$root/.bonus/$domain.inventory" ]] || return 1
  IFS=$'\t' read -r uuid store <"$root/.bonus/$domain.inventory" || return 1
  [[ $uuid =~ ^[[:xdigit:]-]{36}$ ]] || return 1
  [[ $store == "/var/lib/libvirt/images/gobank-bonus/$domain-$uuid" ]] || return 1
  [[ $(v domuuid "$domain") == "$uuid" ]]
}
assert_off() {
  [[ $(v domstate "$uuid") == 'shut off' ]]
}
