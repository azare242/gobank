#!/usr/bin/env bash
# Run on the libvirt host. Supply a pristine Debian cloud image and verified SHA512.
set -euo pipefail
[[ $# -eq 4 ]] || { echo "Usage: $0 DOMAIN CLOUD_IMAGE SHA512 SSH_PUBLIC_KEY" >&2; exit 2; }
root="$(cd "$(dirname "$0")/../.." && pwd)"
domain=$1
image=$(realpath "$2")
checksum=$3
public_key=$4
case "$domain" in
  gobank-cp) cpus=2; ram=2048; disk=20; mac=52:54:00:83:00:10 ;;
  gobank-w1) cpus=4; ram=4096; disk=30; mac=52:54:00:83:00:11 ;;
  gobank-w2) cpus=4; ram=4096; disk=30; mac=52:54:00:83:00:12 ;;
  *) exit 2 ;;
esac
[[ $checksum =~ ^[a-fA-F0-9]{128}$ ]]
[[ -r /dev/kvm && -w /dev/kvm ]]
firmware=/usr/share/OVMF/OVMF_CODE_4M.fd
[[ -r $firmware ]] || { echo "Install the ovmf package for UEFI guest boot." >&2; exit 1; }
for tool in virsh virt-install qemu-img cloud-localds; do command -v "$tool" >/dev/null; done
printf '%s  %s\n' "$checksum" "$image" | sha512sum --check --status
ssh-keygen -lf "$public_key" >/dev/null
key=$(awk 'NR == 1 { print $1, $2 }' "$public_key")
[[ $key =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521))\ [A-Za-z0-9+/=]+$ ]]
v() { LC_ALL=C virsh -c qemu:///system "$@"; }
if v dominfo "$domain" >/dev/null 2>&1; then
  echo "$domain already exists" >&2
  exit 1
fi
if ! v net-info gobank-bonus >/dev/null 2>&1; then
  v net-define "$root/deploy/vm/network.xml"
fi
network=$(v net-info gobank-bonus)
if ! awk '$1 == "Active:" && $2 == "yes" { found=1 } END { exit !found }' <<<"$network"; then
  v net-start gobank-bonus
fi
v net-autostart gobank-bonus
umask 077
state="$root/.bonus"
mkdir -p "$state"
work=$(mktemp -d "$state/cloud-init.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
uuid=$(cat /proc/sys/kernel/random/uuid)
store="/var/lib/libvirt/images/gobank-bonus/$domain-$uuid"
cat >"$work/user-data" <<CLOUD
#cloud-config
hostname: $domain
manage_etc_hosts: true
ssh_pwauth: false
users:
  - name: debian
    lock_passwd: true
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    ssh_authorized_keys:
      - $key
CLOUD
printf 'instance-id: %s\nlocal-hostname: %s\n' "$uuid" "$domain" >"$work/meta-data"
cloud-localds "$work/seed.iso" "$work/user-data" "$work/meta-data"
# Let QEMU traverse these directories without exposing their listings.
sudo install -d -m 0711 "$(dirname "$store")" "$store"
# Convert to an independent disk so guest changes cannot touch the shared image.
sudo qemu-img convert -O qcow2 "$image" "$store/os.qcow2"
sudo qemu-img resize "$store/os.qcow2" "${disk}G"
sudo chown libvirt-qemu "$store/os.qcow2"
sudo chmod 0600 "$store/os.qcow2"
sudo install -m 0644 "$work/seed.iso" "$store/seed.iso"
printf '%s\t%s\n' "$uuid" "$store" >"$state/$domain.inventory"
virt-install --connect qemu:///system --name "$domain" --uuid "$uuid" \
  --virt-type kvm --memory "$ram" --vcpus "$cpus" --import \
  --boot "loader=$firmware,loader.type=pflash,loader.readonly=yes,loader.stateless=yes" \
  --osinfo debian13 --graphics none --noautoconsole \
  --disk "path=$store/os.qcow2,format=qcow2,bus=virtio" \
  --disk "path=$store/seed.iso,device=cdrom" \
  --network "network=gobank-bonus,mac=$mac"
v domblklist "$domain" --details
