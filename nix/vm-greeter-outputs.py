machine.wait_for_unit("vkms-outputs.service", timeout=seconds(60))

with logs_on_failure(machine, greeter_logs):
    machine.succeed("udevadm settle --timeout=10")
    machine.succeed("systemd-run --no-ask-password --unit=splash -p Type=notify lys-splash --mode=boot")
    machine.succeed("lys-splash pause-progress")
    machine.sleep(1)
    splash = grab(machine, "/dev/dri/card0", "splash")
    assert sorted(splash) == ["Virtual-1", "Virtual-2"], f"the splash lit {sorted(splash)}"
    for output, shot in splash.items():
        content(shot, (0, 0, 0))

    machine.succeed("touch /run/greeter-go")
    machine.wait_until_succeeds("journalctl -b -u lys-splash-start -u splash -g 'msg=quitting mode=boot retain=true'", timeout=seconds(90))

    shots = []

    def both_taken():
        shots.append(grab(machine, "/dev/dri/card0", f"greeter{len(shots)}"))
        shown = shots[-1]
        still = [output for output in splash if differences(shown[output], splash[output]) is None]
        return shown, f"{still} still show the splash" if still else None

    await_until(20, both_taken)

    def settled():
        shots.append(grab(machine, "/dev/dri/card0", f"greeter{len(shots)}"))
        changed = [output for output in splash if differences(shots[-1][output], shots[-2][output]) is not None]
        return shots[-1], f"{changed} still change" if changed else None

    for output, shot in await_until(20, settled).items():
        shot.save(machine.out_dir / f"greeter-{output}.png")
    held = machine.execute("journalctl -b -g 'no client drew on'")
    assert held[0] != 0, f"lys-wm gave up on a frame no client drew over: {held[1]}"
