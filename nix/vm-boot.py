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


explained = "journalctl -b --no-pager -o short-monotonic -u 'lys-splash*' -u 'ask-*' -u greeter; echo '--- tty1'; tr -s ' ' < /dev/vcs1"

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
    machine.succeed("grep -qw splash /proc/cmdline", "grep -q 'accent: .#ffb454' /etc/lys-splash/theme.yaml")

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
    for unit in ("lys-splash-quit", "getty@tty1"):
        state = machine.succeed(f"systemctl show -P ActiveState {unit}").strip()
        assert state == "inactive", f"{unit} is {state} under the greeter"
    machine.succeed("touch /run/greeter-go")
    machine.wait_for_unit("greeter.service", timeout=seconds(10))
    machine.wait_until_fails(f"kill -0 {pid}", timeout=seconds(5))
    machine.wait_for_unit("lys-splash-quit-wait.service", timeout=seconds(10))
    machine.sleep(1)
    assert_same(screenshot(machine, "handed-over"), paused, "the frame after the hand-over against the splash before it")
    in_order(machine,
        ("lys-splash-read-write.service", r"^Finished "),
        ("lys-splash-quit-wait.service", r"^Finished "))
    recorded_duration(machine)
    in_order(machine,
        ("lys-splash-start.service", r'msg="showing the splash again"'),
        ("lys-splash-start.service", r"msg=quitting mode=boot retain=true"),
        ("greeter.service", r"^Started "))
    machine.succeed("systemctl stop greeter")

try:
    reboot_shows_splash(machine, splash)
except BaseException:
    print("--- machine's console\n" + machine.get_console_log()[-30000:])
    raise
finally:
    machine.crash()
