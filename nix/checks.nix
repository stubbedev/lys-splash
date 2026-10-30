{self, ...}: {
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
    module = self.nixosModules.default;
  in {
    checks = {
      package = self'.packages.default;
      plymouth-client = self'.packages.default.overrideAttrs (old: {
        pname = "lys-splash-plymouth-client";
        env = old.env // {LYS_SPLASH_PLYMOUTH = lib.getExe' pkgs.plymouth "plymouth";};
        checkFlags = ["-run=^TestPlymouthClient$" "-v"];
      });
      vm-kms = import ./vm-kms.nix {
        inherit pkgs;
        splash = self'.packages.default;
        kms = testBinary "kms" "internal/kms";
      };
      vm-kms-switch = import ./vm-kms-switch.nix {
        inherit pkgs;
        splash = self'.packages.default;
      };
      units = import ./units-check.nix {
        inherit pkgs;
        splash = self'.packages.default;
      };
      module = import ./module-check.nix {inherit pkgs module;};
      vm-boot = import ./vm-boot.nix {
        inherit pkgs module;
        splash = self'.packages.default;
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
