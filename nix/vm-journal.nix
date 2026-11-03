{pkgs}:
pkgs.testers.runNixOSTest {
  name = "lys-splash-journal";
  extraPythonPackages = p: [p.pillow];
  nodes.machine = {};
  testScript =
    builtins.readFile ./screens.py
    + builtins.readFile ./stages.py
    + builtins.readFile ./vm-journal.py;
}
