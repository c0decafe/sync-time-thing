{
  inputs = {
    devenv.url = "github:cachix/devenv";
    flake-parts.url = "github:hercules-ci/flake-parts";
    nix2container = {
      url = "github:nlewo/nix2container";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    nixpkgs.url = "github:cachix/devenv-nixpkgs/rolling";
  };

  outputs = inputs@{ flake-parts, devenv, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" ];
      imports = [ devenv.flakeModule ];

      perSystem = { config, pkgs, system, ... }: {
        devenv.shells.default = import ./devenv.nix {
          inherit config inputs pkgs system;
          lib = pkgs.lib;
        };
      };
    };
}
