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
      # The daemon end to end on a virtio-gpu VM: it lights the output and
      # takes tty1, the plymouth client and systemd's password agent talk
      # to it, a password typed on the VM's keyboard comes back, and quit
      # with --retain-splash leaves the frame for a screenshot.
      vm-splash = let
        splash = self'.packages.default;
      in
        pkgs.testers.runNixOSTest {
          name = "lys-splash-daemon";
          nodes.machine = {
            virtualisation.qemu.options = ["-vga none" "-device virtio-gpu-pci"];
            systemd.services."getty@tty1".enable = false;
            systemd.services."autovt@tty1".enable = false;
            systemd.suppressedSystemUnits = [
              "systemd-ask-password-console.path"
              "systemd-ask-password-console.service"
              "systemd-ask-password-wall.path"
              "systemd-ask-password-wall.service"
            ];
            environment.systemPackages = [splash];
            systemd.tmpfiles.rules = ["L+ /run/plymouth-link/plymouth - - - - ${splash}/bin/lys-splash"];
          };
          testScript = ''
            machine.wait_for_unit("multi-user.target")
            machine.succeed("systemd-run --no-ask-password --unit=splash -p Type=notify -p KillMode=mixed lys-splash --mode=boot --pid-file=/run/lys-splash/pid")
            machine.succeed("test -s /run/lys-splash/pid")
            machine.succeed("lys-splash --ping")
            machine.succeed("/run/plymouth-link/plymouth --ping")
            machine.succeed("lys-splash --has-active-vt")
            machine.succeed("lys-splash display-message --text='Starting Disk Encryption'")
            machine.succeed("systemd-run --no-ask-password --unit=ask -p StandardOutput=file:/tmp/answer lys-splash ask-for-password --prompt='Disk password'")
            machine.sleep(1)
            machine.screenshot("prompt")
            machine.send_chars("hunter2\n")
            machine.wait_until_succeeds("test \"$(cat /tmp/answer)\" = hunter2")
            machine.succeed("test \"$(lys-splash ask-for-password --command='sh -c cat' 2>&1)\" = hunter2")
            machine.succeed("systemd-run --no-ask-password --unit=agent-request -p StandardOutput=file:/tmp/agent systemd-ask-password --no-tty 'Root disk:'")
            machine.wait_until_succeeds("ls /run/systemd/ask-password/ask.*")
            machine.succeed("systemd-run --no-ask-password --unit=agent systemd-tty-ask-password-agent --plymouth --query")
            machine.sleep(1)
            machine.send_chars("s3cret\n")
            machine.wait_until_succeeds("test \"$(cat /tmp/agent)\" = s3cret")
            machine.succeed("lys-splash quit --retain-splash")
            machine.wait_until_fails("lys-splash --ping")
            machine.fail("test -e /run/lys-splash/pid")
            machine.screenshot("retained")
            rc, _ = machine.execute("lys-splash --ping")
            assert rc == 3, f"--ping with no daemon exited {rc}, want 3"
          '';
        };
    };
  };
}
