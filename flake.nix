{
  description = "lys-splash - the lys desktop's boot splash";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    lys-wm = {
      url = "github:stubbedev/lys-wm";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
    };
    lys-login = {
      url = "github:stubbedev/lys-login";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-parts.follows = "flake-parts";
      inputs.lys-wm.follows = "lys-wm";
    };
  };

  outputs = inputs @ {
    flake-parts,
    nixpkgs,
    ...
  }:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = nixpkgs.lib.systems.flakeExposed;
      imports = [
        ./nix/package.nix
        ./nix/shell.nix
        ./nix/checks.nix
        ./nix/module.nix
      ];
    };
}
