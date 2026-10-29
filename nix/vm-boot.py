import json
import re
from datetime import timedelta


def seconds(n):
    return timedelta(seconds=n)


def journal(m):
    out = m.succeed("journalctl -b --no-pager -o json --output-fields=MESSAGE,UNIT,_SYSTEMD_UNIT")
    entries = []
    for line in out.splitlines():
        e = json.loads(line)
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


def booted(m):
    m.wait_for_unit("lys-splash-read-write.service", timeout=seconds(90))
    m.wait_until_succeeds("systemctl show -P ActiveState lys-splash-quit-wait | grep -qx activating", timeout=seconds(30))


def ask(m, prompt, keys):
    unit = f"ask-{m.name}"
    m.succeed(f"systemd-run --no-ask-password --unit={unit} -p StandardOutput=file:/tmp/answer lys-splash ask-for-password --prompt='{prompt}'")
    m.wait_until_succeeds(f"systemctl is-active {unit}", timeout=seconds(5))
    m.sleep(1)
    m.send_chars(keys)
    m.wait_until_fails(f"systemctl is-active {unit}", timeout=seconds(10))
    return m.succeed("cat /tmp/answer")


def recorded_duration(m):
    duration = float(m.succeed("cat /var/lib/lys-splash/boot-duration"))
    assert duration > 0, f"{m.name} recorded a boot of {duration}s"


def top_half(img):
    return img.crop((0, 0, img.width, img.height // 2))


explained = "journalctl -b --no-pager -o short-monotonic -u 'lys-splash*' -u 'ask-*' -u next-master; echo '--- tty1'; tr -s ' ' < /dev/vcs1"

start_all()

with logs_on_failure(text, explained):
    booted(text)
    text.succeed("test ! -e /dev/dri/card0", "lys-splash --ping")
    answer = ask(text, "Text password", "hunter2\n")
    assert answer == "hunter2", f"the text fallback's password arrived as {answer!r}"
    text.succeed("grep -q 'Text password' /dev/vcs1")
    text.succeed("echo 'Started Lys Marker Service.' > /dev/console")
    text.sleep(1)
    assert "Lys Marker" not in text.get_console_log(), "the console's output was not captured"
    text.succeed("systemctl start lys-splash-quit")
    text.wait_for_unit("lys-splash-quit-wait.service", timeout=seconds(10))
    text.wait_for_console_text("Started Lys Marker Service", timeout=seconds(5))
    in_order(text,
        ("lys-splash-read-write.service", r"^Finished "),
        ("lys-splash-quit.service", r"^Starting "),
        ("lys-splash-quit-wait.service", r"^Finished "))
    recorded_duration(text)

with logs_on_failure(machine, explained):
    booted(machine)
    machine.succeed(f"systemd-analyze verify --man=no {units}/*")

    pid = machine.succeed("cat /run/plymouth/pid").strip()
    in_order(machine,
        ("lys-splash-start.service", rf'msg="showing the splash" mode=boot pid={pid}$'),
        ("lys-splash-ask-password.path", r"^Started "),
        ("lys-splash-switch-root.service", r"^Finished "),
        (None, r"^Switching root\.$"),
        ("lys-splash-read-write.service", r"^Finished "))
    assert machine.succeed(f"head -c1 /proc/{pid}/cmdline") == "@", "the splash in the real root is not marked to survive the switch"
    machine.succeed(f"test \"$(readlink /proc/{pid}/root)\" = /")
    machine.succeed("lys-splash --ping", "plymouth --ping", "lys-splash --has-active-vt")
    splash = screenshot(machine, "splash")

    answer = ask(machine, "Disk password", "yz-\n")
    assert answer == "zyß", f"the German layout's password arrived as {answer!r}, want 'zyß'"

    machine.succeed("echo 'Started Lys Marker Service.' > /dev/console")
    machine.send_key("esc")
    machine.wait_until_succeeds("journalctl -b -u lys-splash-start -g 'showing the boot messages'", timeout=seconds(5))
    machine.wait_for_text("Lys Marker", timeout=seconds(20))
    machine.screenshot("boot-messages")
    machine.send_key("esc")
    machine.wait_until_succeeds("journalctl -b -u lys-splash-start -g 'showing the splash again'", timeout=seconds(5))
    await_screen(machine, "splash-again", lambda _, cur: differences(top_half(cur), top_half(splash)), 10)

    machine.succeed("lys-splash pause-progress")
    machine.sleep(1)
    paused = screenshot(machine, "paused")
    machine.succeed("lys-splash deactivate")
    machine.succeed("systemd-run --no-ask-password --unit=next-master sh -c 'exec 3<>/dev/dri/card0; exec /run/current-system/sw/bin/sleep infinity'")
    machine.wait_until_succeeds("ls -l /proc/$(systemctl show -P MainPID next-master)/fd | grep -q dri/card0", timeout=seconds(5))
    machine.succeed("lys-splash quit --retain-splash")
    machine.wait_until_fails(f"kill -0 {pid}", timeout=seconds(5))
    machine.wait_for_unit("lys-splash-quit-wait.service", timeout=seconds(10))
    machine.sleep(1)
    assert_same(screenshot(machine, "handed-over"), paused, "the frame after the hand-over against the splash before it")
    in_order(machine,
        ("lys-splash-read-write.service", r"^Finished "),
        ("lys-splash-quit-wait.service", r"^Finished "))
    recorded_duration(machine)
    machine.succeed("systemctl stop next-master")

try:
    machine.execute("systemctl --no-block reboot")
    machine.wait_for_console_text(r'msg="showing the splash" mode=reboot', timeout=seconds(30))
    machine.wait_for_console_text(r"reboot: Restarting system", timeout=seconds(60))
    await_until(10, lambda: (None, None if "paused" in machine.send_monitor_command("info status") else "the VM has not paused at the reset"))
    rebooting = screenshot(machine, "rebooting")
    assert_same(top_half(rebooting), top_half(splash), "the frame at the reset against the splash's top half")
    assert "msg=quitting mode=reboot" not in machine.get_console_log(), "the reboot splash quit before the reset"
except BaseException:
    print("--- machine's console\n" + machine.get_console_log()[-30000:])
    raise
finally:
    machine.crash()
