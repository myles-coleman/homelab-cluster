{ config, pkgs, lib, ... }:

{
  imports = [
    ./k3s-common.nix
    ./k3s-token-secrets.nix
  ];

  services.k3s = {
    enable = true;
    role = "agent";
    serverAddr = "https://10.0.0.200:6443";
    tokenFile = "/etc/rancher/k3s/token";
  };
}
