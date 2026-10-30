import re
from datetime import timedelta


def seconds(n):
    return timedelta(seconds=n)


modeset = re.compile(r"Set \[MODE:|Link \[CONNECTOR:|mode changed|active changed|enable changed|connectors changed|requires full modeset")


def dump(i):
    path = machine.out_dir / f"handover-{i:04d}.ppm"
    machine.send_monitor_command(f"screendump {path}")
    return path


def black(img):
    return all(high < 16 for _, high in img.getextrema())


def share(box, img):
    if box is None:
        return 0
    return (box[2] - box[0]) * (box[3] - box[1]) / (img.width * img.height)


explained = "journalctl -b --no-pager -o short-monotonic -u 'lys-splash*' -u lys-login -u greeter-gate -u user@998; journalctl -b --no-pager -o short-monotonic _UID=998 | tail -50"

start_all()

with logs_on_failure(machine, explained):
    machine.wait_for_unit("lys-splash-read-write.service", timeout=seconds(90))
    machine.wait_until_succeeds("systemctl show -P ActiveState greeter-gate | grep -qx activating", timeout=seconds(60))
    pid = machine.succeed("cat /run/plymouth/pid").strip()
    machine.succeed("lys-splash pause-progress")
    machine.sleep(1)
    splash = screenshot(machine, "splash")
    content(splash, (0, 0, 0))
    for unit in ("lys-splash-quit", "getty@tty1"):
        state = machine.succeed(f"systemctl show -P ActiveState {unit}").strip()
        assert state == "inactive", f"{unit} is {state} under lys-login"

    machine.succeed("touch /run/greeter-go")
    paths = []
    quit_at = None
    deadline = time.monotonic() + 180
    while quit_at is None or len(paths) < quit_at + 40:
        paths.append(dump(len(paths)))
        time.sleep(0.04)
        if quit_at is None and len(paths) % 10 == 0 and machine.execute(f"kill -0 {pid}")[0] != 0:
            quit_at = len(paths)
        assert time.monotonic() < deadline, f"the splash still runs 180s after lys-login was let start, {len(paths)} dumps in"

    frames = [load(p) for p in paths]
    greeter_at = None
    for i, frame in enumerate(frames):
        if differences(frame, splash) is None:
            assert greeter_at is None, f"dump {i} shows the splash again after the greeter showed in dump {greeter_at}"
            continue
        assert not black(frame), f"dump {i} of {len(frames)} is black"
        if greeter_at is None:
            greeter_at = i
    assert greeter_at is not None, f"none of {len(frames)} dumps shows the greeter"
    greeter = frames[-1]
    frames[greeter_at].save(machine.out_dir / "greeter-first.png")
    greeter.save(machine.out_dir / "greeter-last.png")
    for i in range(greeter_at, len(frames)):
        box = ImageChops.difference(frames[i], greeter).getbbox()
        assert share(box, greeter) < 0.1, f"dump {i}, after the splash, is neither the splash nor the greeter: it differs from the greeter in {box}"
    print(f"{len(frames)} dumps: the splash until dump {greeter_at}, the greeter from there")

    kernel = machine.succeed("dmesg --notime")
    assert marker in kernel, "the gate left no mark in the kernel log"
    drm = [line for line in kernel[kernel.rindex(marker):].splitlines()[1:] if "[drm:" in line]
    commits = [line for line in drm if "] committing " in line]
    assert commits, "nothing committed after the splash dropped master"
    assert commits[0].endswith(" nonblocking"), f"the first commit after the splash dropped master is not lys-wm's flip: {commits[0]}"
    modesets = [line for line in drm if modeset.search(line)]
    assert not modesets, f"the hand-over modeset: {modesets[:8]}"

    machine.succeed("journalctl -b -u lys-splash-start -g 'msg=quitting mode=boot retain=true'")
    for unit in ("lys-splash-quit", "getty@tty1"):
        state = machine.succeed(f"systemctl show -P ActiveState {unit}").strip()
        assert state == "inactive", f"{unit} is {state} after the hand-over"
    machine.succeed("pgrep -u lys-greeter -f 'lys-login greet'")
