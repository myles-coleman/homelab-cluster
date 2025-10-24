{ config, pkgs, lib, ... }:

let
  k3sTokenPath = ./token;
  k3sToken = if builtins.pathExists k3sTokenPath
    then lib.removeSuffix "\n" (builtins.readFile k3sTokenPath)
    else "";
in
{
  environment.etc."rancher/k3s/token" = lib.mkIf (k3sToken != "") {
    text = k3sToken;
    mode = "0600";
  };
}
