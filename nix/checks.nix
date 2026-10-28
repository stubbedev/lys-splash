{
  perSystem = {
    lib,
    pkgs,
    self',
    ...
  }: {
    checks = {
      package = self'.packages.default;
      plymouth-client = self'.packages.default.overrideAttrs (old: {
        pname = "lys-splash-plymouth-client";
        env = old.env // {LYS_SPLASH_PLYMOUTH = lib.getExe' pkgs.plymouth "plymouth";};
        checkFlags = ["-run=^TestPlymouthClient$" "-v"];
      });
    };
  };
}
