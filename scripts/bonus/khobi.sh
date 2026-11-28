
id -nG
test -r /dev/kvm && test -w /dev/kvm && echo "KVM device accessible"
virsh -c qemu:///system list --all
virt-host-validate qemu