{ config, pkgs, lib, ... }:

{
  services.k3s.package = pkgs.k3s_1_33;
  
  environment.systemPackages = with pkgs; [
    k3s
    coreutils
    openiscsi
    cryptsetup
    util-linux
    nfs-utils
    vim
    htop
    kubectl
    curl
  ];

  boot.kernelModules = [
    "br_netfilter"
    "overlay"
  ];

  boot.kernelParams = [
    "cgroup_enable=cpuset"
    "cgroup_memory=1"
    "cgroup_enable=memory"
  ];

  boot.kernel.sysctl = {
    "net.ipv4.ip_forward" = 1;
    "net.ipv6.conf.all.forwarding" = 1;
    "net.ipv6.conf.all.accept_ra" = 2;
    "net.bridge.bridge-nf-call-iptables" = 1;
    "net.bridge.bridge-nf-call-ip6tables" = 1;
  };

  networking.firewall.enable = false;
  networking.nftables.enable = false;

  services.openiscsi = {
    enable = true;
    name = "iqn.2016-04.com.open-iscsi:${config.networking.hostName}";
  };

  # symlink iscsiadm for Longhorn
  system.activationScripts.longhorn-iscsiadm = ''
    mkdir -p /usr/bin
    ln -sf ${pkgs.openiscsi}/bin/iscsiadm /usr/bin/iscsiadm
  '';

  systemd.services.k3s.after = [ "network-online.target" ];
  systemd.services.k3s.wants = [ "network-online.target" ];
}
