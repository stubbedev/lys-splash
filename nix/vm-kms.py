import re
from datetime import timedelta

lit = re.compile(r'msg="lit an output" card=card0 connector=(\S+) size=\((\d+),(\d+)\) scale=(\d+) rotation="?([a-z ]+?)"? "kept')

machine.wait_for_unit("vkms-outputs.service", timeout=timedelta(seconds=60))

with logs_on_failure(machine):
    machine.succeed("udevadm settle --timeout=10")
    machine.succeed("test \"$(readlink -f /sys/class/drm/card0/device)\" = /sys/devices/faux/splash", "test ! -e /sys/class/drm/card1")

    out = machine.succeed(f"LYS_SPLASH_KMS_VM=1 {kms_test} -test.v -test.count=1 -test.run TestRealCard 2>&1")
    print(out)
    assert "--- PASS: TestRealCardFlipsDeactivatesAndRetains" in out, "the real-card test did not pass"

    machine.succeed("systemd-run --no-ask-password --unit=splash -p Type=notify lys-splash --mode=boot")
    machine.succeed("lys-splash pause-progress")
    machine.succeed("lys-splash display-message --text='Two outputs'")

    outputs = {o[0]: o[1:] for o in lit.findall(machine.succeed("journalctl --no-pager -o cat -u splash"))}
    want = {"Virtual-1": ("1024", "768", "1", "upright"), "Virtual-2": ("1024", "768", "1", "clockwise")}
    assert outputs == want, f"the splash lit {outputs}, want {want}"

    grabs = []

    def steady_grab():
        grabs.append(grab(machine, "/dev/dri/card0", f"grab{len(grabs)}"))
        if len(grabs) < 2:
            return grabs[-1], "one grab is not enough to call it steady"
        prev, cur = grabs[-2], grabs[-1]
        if prev.keys() != cur.keys():
            return cur, f"the outputs went from {sorted(prev)} to {sorted(cur)}"
        for name in cur:
            diff = differences(prev[name], cur[name])
            if diff is not None:
                return cur, f"{name} changed between two grabs in {diff}"
        return cur, None

    shots = await_until(10, steady_grab)
    assert sorted(shots) == ["Virtual-1", "Virtual-2"], f"drm-grab found {sorted(shots)}"
    upright, turned = shots["Virtual-1"], shots["Virtual-2"]
    black = (0, 0, 0)

    box = content(upright, black)
    unturned = turned.transpose(Image.Transpose.ROTATE_90)
    turned_box = content(unturned, black)
    print(f"Virtual-1 shows the splash in {box}, Virtual-2 turned back in {turned_box}")
    assert abs(box[0] + box[2] - upright.width) <= 1, f"the splash on Virtual-1 is not centred: {box}"
    assert abs(turned_box[0] + turned_box[2] - unturned.width) <= 1, f"the splash on Virtual-2 is not centred: {turned_box}"
    assert_same(upright.crop(box), unturned.crop(turned_box), "the splash on Virtual-1 and the splash on Virtual-2 turned back")

    raw = content(turned, black)
    assert (raw[2] - raw[0], raw[3] - raw[1]) == (box[3] - box[1], box[2] - box[0]), f"Virtual-2's scanout shows the splash in {raw}, not turned"
