import contextlib


@contextlib.contextmanager
def stage(m, name, logs="journalctl -b --no-pager -o short-monotonic -u 'lys-splash*' -u 'systemd-cryptsetup@*'"):
    try:
        yield
    except BaseException as e:
        try:
            _, out = m.execute(f"{{ {logs}; }} 2>&1", timeout=30)
            print(f"--- {m.name}: {logs}\n{out}")
        except BaseException:
            print(f"--- {m.name}'s console\n" + m.get_console_log()[-20000:])
        raise AssertionError(f"{m.name}: the {name} stage failed: {e}") from e


def encrypt(m, disks):
    m.wait_for_unit("lys-splash-read-write.service", timeout=seconds(120))
    for i, disk in enumerate(disks):
        m.succeed(
            f"echo -n {passphrase} | cryptsetup luksFormat -q --iter-time=1 {disk} -",
            f"echo -n {passphrase} | cryptsetup luksOpen -q {disk} crypt{i}",
            f"mkfs.ext4 -q /dev/mapper/crypt{i}",
        )
    m.succeed("/run/current-system/specialisation/luks/bin/switch-to-configuration boot", "sync")
    m.crash()
    m.start()


def type_passphrase(m):
    m.wait_for_console_text(r"Starting .*Cryptography Setup for .*crypt0", timeout=seconds(120))
    time.sleep(3)
    m.send_chars(passphrase + "\n")


def unlocked(m, mounts):
    m.wait_for_unit("lys-splash-read-write.service", timeout=seconds(120))
    table = m.succeed("mount")
    for device, at in mounts:
        assert f"/dev/mapper/{device} on {at} type ext4" in table, f"/dev/mapper/{device} is not mounted on {at}:\n{table}"


with stage(text, "LUKS setup"):
    encrypt(text, ["/dev/vdb"])

with stage(text, "text passphrase"):
    type_passphrase(text)
    unlocked(text, [("crypt0", "/")])
    text.succeed("test ! -e /dev/dri/card0")
    text.succeed("journalctl -b -u lys-splash-ask-password.service -g 'Started '")
    text.fail("journalctl -b -u systemd-ask-password-console.service -g 'Started '")

text.shutdown()

with stage(machine, "LUKS setup"):
    encrypt(machine, ["/dev/vdb", "/dev/vdc"])

with stage(machine, "passphrase"):
    type_passphrase(machine)
    machine.screenshot("typed")

with stage(machine, "cached passphrase"):
    unlocked(machine, [("crypt0", "/"), ("crypt1", "/data")])

with stage(machine, "simpledrm to virtio-gpu"):
    lit = machine.succeed("journalctl -b -o cat -u lys-splash-start -g 'lit an output'").splitlines()
    assert len(lit) >= 2, f"the splash lit {lit}, not first simpledrm and then virtio-gpu"
    machine.succeed("journalctl -b -o cat -u lys-splash-start -g 'a card went away'")
    machine.succeed("test $(cat /sys/firmware/acpi/bgrt/status) = 1")

with stage(machine, "survival"):
    pid = machine.succeed("cat /run/plymouth/pid").strip()
    in_order(machine,
        ("lys-splash-start.service", rf'msg="showing the splash" mode=boot pid={pid}$'),
        (None, r"^Switching root\.$"),
        ("lys-splash-read-write.service", r"^Finished "))
    assert machine.succeed(f"head -c1 /proc/{pid}/cmdline") == "@", "the splash in the real root is not marked to survive the switch"
    machine.succeed(f"test \"$(readlink /proc/{pid}/root)\" = /")

with stage(machine, "compatibility"):
    machine.succeed(f"{plymouth} --ping", f"{plymouth} display-message --text='nixpkgs plymouth'")
    machine.succeed("journalctl -b -u lys-splash-ask-password.service -g 'Started '")

with stage(machine, "hand-over", greeter_logs):
    await_gate(machine)
    hand_over(machine)

with stage(machine, "shutdown"):
    try:
        reboot_shows_splash(machine, load(machine.out_dir / "splash.png"))
    finally:
        machine.crash()
