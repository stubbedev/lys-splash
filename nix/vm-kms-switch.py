import re
from datetime import timedelta

lit = re.compile(r'msg="lit an output" card=(card\d+) connector=(\S+) size=\((\d+),(\d+)\) scale=(\d+)')


def lit_outputs():
    return lit.findall(machine.succeed("journalctl --no-pager -o cat -u splash"))


def driver_of(card):
    return machine.succeed(f"basename $(readlink /sys/class/drm/{card}/device/driver)").strip()


def logo_region(shot):
    return shot.crop((bgrt_at[0], bgrt_at[1], bgrt_at[0] + bgrt.width, bgrt_at[1] + bgrt.height))


machine.start()
machine.wait_for_unit("multi-user.target", timeout=timedelta(seconds=120))

with logs_on_failure(machine):
    assert driver_of("card0") == "simple-framebuffer", f"card0 is driven by {driver_of('card0')}, not simpledrm"
    assert machine.succeed("cat /sys/firmware/acpi/bgrt/status").strip() == "1", "OVMF left no BGRT logo on screen"
    bgrt_at = tuple(int(machine.succeed(f"cat /sys/firmware/acpi/bgrt/{axis}offset")) for axis in "xy")
    machine.copy_from_machine("/sys/firmware/acpi/bgrt/image", "bgrt")
    bgrt = load(machine.out_dir / "bgrt" / "image")
    print(f"the BGRT logo is {bgrt.size} at {bgrt_at}")

    firmware = await_screen(machine, "firmware", steady_and(lambda _: None), 10)
    assert_same(logo_region(firmware), bgrt, "the BGRT image and its place in the firmware's last frame")

    machine.succeed("systemd-run --no-ask-password --unit=splash -p Type=notify lys-splash --mode=boot")
    machine.succeed("lys-splash pause-progress")
    first = lit_outputs()
    assert [o[0] for o in first] == ["card0"], f"the splash lit {first} on simpledrm"

    def splash_drawn(shot):
        if differences(firmware, shot) is None:
            return "the screen still shows the firmware's frame"
        return None

    simpledrm = await_screen(machine, "simpledrm", steady_and(splash_drawn), 10)
    assert_same(logo_region(simpledrm), bgrt, "the BGRT image and its place in the splash's frame on simpledrm")

    machine.succeed("modprobe virtio_gpu")

    def lit_twice():
        outputs = lit_outputs()
        return outputs, None if len(outputs) == 2 else f"the splash lit {outputs}"

    native = await_until(10, lit_twice)[1]
    assert 'msg="a card went away" card=card0' in machine.succeed("journalctl --no-pager -o cat -u splash"), "the splash did not drop simpledrm's card"
    assert driver_of(native[0]) == "virtio-pci", f"{native[0]} is driven by {driver_of(native[0])}, not virtio-gpu"
    assert native[2:] == first[0][2:], f"simpledrm lit {first[0]}, virtio-gpu {native}: the size or the scale changed"

    await_screen(machine, "native", matching(simpledrm, "the splash on simpledrm"), 10)
    machine.succeed("lys-splash --ping")
