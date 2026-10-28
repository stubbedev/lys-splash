{
  perSystem = {
    lib,
    pkgs,
    self',
    ...
  }: let
    testBinary = name: pkg:
      self'.packages.default.overrideAttrs {
        pname = "lys-splash-${name}-test";
        buildPhase = ''
          runHook preBuild
          go test -c -o ${name}.test ./${pkg}
          runHook postBuild
        '';
        doCheck = false;
        installPhase = ''
          runHook preInstall
          install -Dm755 ${name}.test $out/bin/${name}.test
          runHook postInstall
        '';
      };
  in {
    checks = {
      package = self'.packages.default;
      plymouth-client = self'.packages.default.overrideAttrs (old: {
        pname = "lys-splash-plymouth-client";
        env = old.env // {LYS_SPLASH_PLYMOUTH = lib.getExe' pkgs.plymouth "plymouth";};
        checkFlags = ["-run=^TestPlymouthClient$" "-v"];
      });
      # The kms package on a real card: a NixOS VM whose only DRM device is
      # vkms runs its hardware tests as root, flipping, dropping and taking
      # back master, and closing with and without retaining the frame.
      vm-kms = let
        kms = testBinary "kms" "internal/kms";
      in
        pkgs.testers.runNixOSTest {
          name = "lys-splash-kms";
          nodes.machine = {
            boot.kernelModules = ["vkms"];
            virtualisation.graphics = false;
            virtualisation.qemu.options = ["-vga none"];
          };
          testScript = ''
            machine.wait_for_unit("multi-user.target")
            machine.succeed("udevadm settle", "test \"$(readlink -f /sys/class/drm/card0/device)\" = /sys/devices/faux/vkms", "test ! -e /sys/class/drm/card1")
            out = machine.succeed("LYS_SPLASH_KMS_VM=1 ${kms}/bin/kms.test -test.v -test.count=1 -test.run TestRealCard 2>&1")
            print(out)
            assert "--- PASS: TestRealCardFlipsDeactivatesAndRetains" in out, "the real-card test did not pass"
          '';
        };
    };
  };
}
