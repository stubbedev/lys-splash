{self, ...}: {
  perSystem = {
    lib,
    pkgs,
    ...
  }: {
    packages.default = pkgs.buildGo127Module {
      pname = "lys-splash";
      version = "0-unstable-${self.lastModifiedDate or "0"}";

      src = lib.fileset.toSource {
        root = ../.;
        fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
          lib.fileset.unions [
            ../flake.nix
            ../flake.lock
            ../nix
          ]
        );
      };

      vendorHash = "sha256-ZI8g6AnIvrcW8DwnRV8/k1m7rrLpMPfI3UPFt6Y9Kio=";

      env.CGO_ENABLED = 0;

      doCheck = true;

      meta = {
        description = "The lys desktop's boot splash";
        homepage = "https://github.com/stubbedev/lys-splash";
        license = lib.licenses.mit;
        mainProgram = "lys-splash";
        platforms = lib.platforms.unix;
      };
    };
  };
}
