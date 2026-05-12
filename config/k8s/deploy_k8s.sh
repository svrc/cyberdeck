#!/bin/bash

if [ -e /holodeck-runtime/set_webtop_completed ]; then
    exit
else
    #Iptables filtering IPv4 forwarding
    cat > /etc/sysctl.d/kubernetes.conf << EOF
net.ipv4.ip_forward = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-arptables = 1
net.ipv6.conf.all.disable_ipv6=1
net.ipv6.conf.default.disable_ipv6=1
net.ipv6.conf.lo.disable_ipv6=1
net.ipv4.tcp_l3mdev_accept=0
net.ipv4.udp_l3mdev_accept=1
net.ipv4.conf.all.rp_filter=0
net.ipv4.conf.default.rp_filter=0
EOF

    #Load module
    modprobe br_netfilter

    #Apply the changes
    sysctl --system

    cat > /etc/crictl.yaml << EOF
runtime-endpoint: unix:///run/containerd/containerd.sock
image-endpoint: unix:///run/containerd/containerd.sock
timeout: 2
debug: false
pull-image-on-create: false
disable-pull-on-run: false
EOF

    systemctl restart systemd-networkd
    systemctl daemon-reload
    systemctl restart containerd
    systemctl enable containerd.service

    crictl info | grep -i cgroup | grep true

    systemctl enable --now kubelet

    sleep 5

    #kubeadm config images pull
    for fn in `ls /root/containerd-images`; do
        ctr -n k8s.io image import /root/containerd-images/$fn
    done

    sleep 5

    kubeadm init --pod-network-cidr=10.244.0.0/16

    sleep 15

    export KUBECONFIG=/etc/kubernetes/admin.conf
    #Untaint the control node
    kubectl taint nodes --all node-role.kubernetes.io/control-plane-

    kubectl apply -f /holodeck-runtime/k8s/kube-flannel.yml

    sleep 5
fi
