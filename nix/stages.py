import json
import re

gate_marker = "greeter-gate: the greeter may start"

modeset = re.compile(r"Set \[MODE:|Link \[CONNECTOR:|mode changed|active changed|enable changed|connectors changed|requires full modeset")


def dump(m, i):
    path = m.out_dir / f"handover-{i:04d}.ppm"
    m.send_monitor_command(f"screendump {path}")
    return path


def black(img):
    return all(high < 16 for _, high in img.getextrema())


def share(box, img):
    if box is None:
        return 0
    return (box[2] - box[0]) * (box[3] - box[1]) / (img.width * img.height)


greeter_logs = "journalctl -b --no-pager -o short-monotonic -u 'lys-splash*' -u lys-login -u greeter-gate -u user@998; journalctl -b --no-pager -o short-monotonic _UID=998 | tail -50"


def await_gate(m):
    m.wait_for_unit("lys-splash-read-write.service", timeout=seconds(90))
    m.wait_until_succeeds("systemctl show -P ActiveState greeter-gate | grep -qx activating", timeout=seconds(60))


def no_quit_unit(m, when):
    for unit in ("lys-splash-quit", "getty@tty1"):
        state = m.succeed(f"systemctl show -P ActiveState {unit}").strip()
        assert state == "inactive", f"{unit} is {state} {when}"


def hand_over(m):
    pid = m.succeed("cat /run/plymouth/pid").strip()
    m.succeed("lys-splash pause-progress")
    m.sleep(1)
    splash = screenshot(m, "splash")
    content(splash, (0, 0, 0))
    no_quit_unit(m, "under lys-login")

    m.succeed("touch /run/greeter-go")
    paths = []
    quit_at = None
    deadline = time.monotonic() + 180
    while quit_at is None or len(paths) < quit_at + 40:
        paths.append(dump(m, len(paths)))
        time.sleep(0.04)
        if quit_at is None and len(paths) % 10 == 0 and m.execute(f"kill -0 {pid}")[0] != 0:
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
    frames[greeter_at].save(m.out_dir / "greeter-first.png")
    greeter.save(m.out_dir / "greeter-last.png")
    for i in range(greeter_at, len(frames)):
        box = ImageChops.difference(frames[i], greeter).getbbox()
        assert share(box, greeter) < 0.1, f"dump {i}, after the splash, is neither the splash nor the greeter: it differs from the greeter in {box}"
    print(f"{len(frames)} dumps: the splash until dump {greeter_at}, the greeter from there")

    kernel = m.succeed("dmesg --notime")
    assert gate_marker in kernel, "the gate left no mark in the kernel log"
    drm = [line for line in kernel[kernel.rindex(gate_marker):].splitlines()[1:] if "[drm:" in line]
    commits = [line for line in drm if "] committing " in line]
    assert commits, "nothing committed after the splash dropped master"
    assert commits[0].endswith(" nonblocking"), f"the first commit after the splash dropped master is not lys-wm's flip: {commits[0]}"
    modesets = [line for line in drm if modeset.search(line)]
    assert not modesets, f"the hand-over modeset: {modesets[:8]}"

    m.succeed("journalctl -b -u lys-splash-start -g 'msg=quitting mode=boot retain=true'")
    no_quit_unit(m, "after the hand-over")
    m.succeed("pgrep -u lys-greeter -f 'lys-login greet'")


def reboot_shows_splash(m, splash):
    m.execute("systemctl --no-block reboot", check_return=False, check_output=False)
    m.wait_for_console_text(r'msg="showing the splash" mode=reboot', timeout=seconds(30))
    m.wait_for_console_text(r"reboot: Restarting system", timeout=seconds(60))
    await_until(10, lambda: (None, None if "paused" in m.send_monitor_command("info status") else "the VM has not paused at the reset"))
    rebooting = screenshot(m, "rebooting")
    assert_same(top_half(rebooting), top_half(splash), "the frame at the reset against the splash's top half")
    assert "msg=quitting mode=reboot" not in m.get_console_log(), "the reboot splash quit before the reset"


def journal(m):
    out = m.succeed("journalctl -b --no-pager -o json --output-fields=MESSAGE,UNIT,_SYSTEMD_UNIT")
    entries = []
    for line in filter(None, out.split("\n")):
        e = json.loads(line)
        if "__MONOTONIC_TIMESTAMP" not in e:
            continue
        msg = e.get("MESSAGE") or ""
        if isinstance(msg, list):
            msg = bytes(msg).decode(errors="replace")
        entries.append((int(e["__MONOTONIC_TIMESTAMP"]), e.get("UNIT") or e.get("_SYSTEMD_UNIT") or "", msg))
    return entries


def when(entries, unit, pattern):
    for t, u, msg in entries:
        if (unit is None or u == unit) and re.search(pattern, msg):
            return t
    raise AssertionError(f"the journal has no {pattern!r}" + (f" from {unit}" if unit else ""))


def in_order(m, *steps):
    entries = journal(m)
    times = [when(entries, unit, pattern) for unit, pattern in steps]
    for i in range(1, len(steps)):
        assert times[i - 1] < times[i], f"{steps[i]} at {times[i]}us came before {steps[i - 1]} at {times[i - 1]}us"
