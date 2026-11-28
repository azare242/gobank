#!/usr/bin/env bash
# Run as root inside a fresh Debian 13 guest.
set -euo pipefail
[[ $EUID == 0 ]]
case "$(hostname -s)" in gobank-cp|gobank-w1|gobank-w2) ;; *) exit 2 ;; esac
apt_guest() {
  apt-get -o Acquire::Retries=2 -o Acquire::https::Timeout=20 "$@"
}
apt_guest update
apt_guest install -y containerd curl ca-certificates gpg chrony qemu-guest-agent
swapoff -a
sed -i.bak '/^[^#].*[[:space:]]swap[[:space:]]/s/^/#/' /etc/fstab
printf '%s\n' overlay br_netfilter >/etc/modules-load.d/gobank.conf
modprobe overlay
modprobe br_netfilter
cat >/etc/sysctl.d/90-gobank.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-iptables = 1
SYSCTL
sysctl --system
mkdir -p /etc/containerd /etc/apt/keyrings
containerd config default | sed 's/SystemdCgroup = false/SystemdCgroup = true/; s|bin_dir = "/usr/lib/cni"|bin_dir = "/opt/cni/bin"|' >/etc/containerd/config.toml
systemctl enable --now containerd
systemctl restart containerd
curl --connect-timeout 10 --max-time 60 -fsSL https://pkgs.k8s.io/core:/stable:/v1.34/deb/Release.key |
  gpg --batch --yes --dearmor -o /etc/apt/keyrings/gobank-kubernetes.gpg
printf '%s\n' 'deb [signed-by=/etc/apt/keyrings/gobank-kubernetes.gpg] https://pkgs.k8s.io/core:/stable:/v1.34/deb/ /' >/etc/apt/sources.list.d/kubernetes.list
apt_guest update
apt_guest install -y kubeadm=1.34.3-1.1 kubelet=1.34.3-1.1 kubectl=1.34.3-1.1
apt-mark hold kubeadm kubelet kubectl
systemctl enable --now kubelet
