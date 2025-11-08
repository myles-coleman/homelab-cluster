{
  description = "Multi-node K3s cluster on Raspberry Pi 5";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixos-raspberrypi.url = "github:nvmd/nixos-raspberrypi/main";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  nixConfig = {
    extra-substituters = [
      "https://nixos-raspberrypi.cachix.org"
    ];
    extra-trusted-public-keys = [
      "nixos-raspberrypi.cachix.org-1:4iMO9LXa8BqhU+Rpg6LQKiGa2lsNh/j2oiYLNOQ5sPI="
    ];
  };

  outputs = {
    self,
    nixpkgs,
    nixos-raspberrypi,
    disko,
  } @ inputs: let
    system = "x86_64-linux";
    targetSystem = "aarch64-linux";
    
    baseConfig = [
      nixos-raspberrypi.nixosModules.raspberry-pi-5.base
      nixos-raspberrypi.nixosModules.raspberry-pi-5.page-size-16k
      nixos-raspberrypi.nixosModules.raspberry-pi-5.bluetooth
      nixos-raspberrypi.nixosModules.raspberry-pi-5.display-vc4
      disko.nixosModules.disko
      ./disko-config.nix
      
      ({pkgs, ...}: {
        users.users.pi = {
          initialPassword = "raspberry";
          isNormalUser = true;
          extraGroups = ["wheel"];
        };

        services.openssh = {
          enable = true;
          settings.PasswordAuthentication = true;
          settings.PermitRootLogin = "no";
        };

        # enable mDNS for .local domain resolution
        services.avahi = {
          enable = true;
          nssmdns4 = true;
          publish = {
            enable = true;
            addresses = true;
            domain = true;
            hinfo = true;
            userServices = true;
            workstation = true;
          };
        };

        time.timeZone = "America/Los_Angeles";
        i18n.defaultLocale = "en_US.UTF-8";
        
        hardware.enableRedistributableFirmware = true;
        system.stateVersion = "25.05";
      })
    ];
  in {
    nixosConfigurations.node0 = nixos-raspberrypi.lib.nixosSystem {
      system = targetSystem;
      specialArgs = inputs;
      modules = baseConfig ++ [
        ./k3s-server.nix
        ({...}: {
          networking.hostName = "node0";
          networking.interfaces.end0.ipv4.addresses = [{
            address = "10.0.0.140";
            prefixLength = 24;
          }];
          networking.defaultGateway = "10.0.0.1";
          networking.nameservers = [ "10.0.0.1" ];
        })
      ];
    };

    nixosConfigurations.node1 = nixos-raspberrypi.lib.nixosSystem {
      system = targetSystem;
      specialArgs = inputs;
      modules = baseConfig ++ [
        ./k3s-agent.nix
        ({...}: {
          networking.hostName = "node1";
          networking.interfaces.end0.ipv4.addresses = [{
            address = "10.0.0.141";
            prefixLength = 24;
          }];
          networking.defaultGateway = "10.0.0.1";
          networking.nameservers = [ "10.0.0.1" ];
        })
      ];
    };

    nixosConfigurations.node2 = nixos-raspberrypi.lib.nixosSystem {
      system = targetSystem;
      specialArgs = inputs;
      modules = baseConfig ++ [
        ./k3s-agent.nix
        ({...}: {
          networking.hostName = "node2";
          networking.interfaces.end0.ipv4.addresses = [{
            address = "10.0.0.142";
            prefixLength = 24;
          }];
          networking.defaultGateway = "10.0.0.1";
          networking.nameservers = [ "10.0.0.1" ];
        })
      ];
    };

    nixosConfigurations.node3 = nixos-raspberrypi.lib.nixosSystem {
      system = targetSystem;
      specialArgs = inputs;
      modules = baseConfig ++ [
        ./k3s-agent.nix
        ({...}: {
          networking.hostName = "node3";
          networking.interfaces.end0.ipv4.addresses = [{
            address = "10.0.0.143";
            prefixLength = 24;
          }];
          networking.defaultGateway = "10.0.0.1";
          networking.nameservers = [ "10.0.0.1" ];
        })
      ];
    };

    packages.${system} = {
      node0 = self.nixosConfigurations.node0.config.system.build.sdImage;
      node1 = self.nixosConfigurations.node1.config.system.build.sdImage;
      node2 = self.nixosConfigurations.node2.config.system.build.sdImage;
      node3 = self.nixosConfigurations.node3.config.system.build.sdImage;
      
      default = self.packages.${system}.node0;
    };
  };
}
