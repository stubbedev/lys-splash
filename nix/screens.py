import contextlib
import time
from datetime import timedelta

from PIL import Image, ImageChops

splash_logs = "journalctl --no-pager -o short-monotonic -u splash; dmesg --notime | grep -iE 'drm|fb|vkms|virtio|simple' | tail -n 80"


@contextlib.contextmanager
def logs_on_failure(machine, command=splash_logs):
    try:
        yield
    except BaseException:
        _, logs = machine.execute(f"{{ {command}; }} 2>&1")
        print(f"--- {command}\n{logs}")
        raise


def seconds(n):
    return timedelta(seconds=n)


def top_half(img):
    return img.crop((0, 0, img.width, img.height // 2))


def load(path):
    with Image.open(path) as img:
        return img.convert("RGB")


def screenshot(machine, name):
    machine.screenshot(name)
    return load(machine.out_dir / f"{name}.png")


def grab(machine, card, name):
    machine.succeed("rm -rf /tmp/grab && mkdir /tmp/grab")
    paths = machine.succeed(f"drm-grab {card} /tmp/grab").split()
    shots = {}
    for path in paths:
        machine.copy_from_machine(path, name)
        output = path.removeprefix("/tmp/grab/").removesuffix(".ppm")
        shots[output] = load(machine.out_dir / name / f"{output}.ppm")
    return shots


def differences(a, b):
    if a.size != b.size:
        return f"{a.size} against {b.size}"
    box = ImageChops.difference(a, b).getbbox()
    if box is None:
        return None
    return f"pixels in {box}"


def assert_same(a, b, what):
    diff = differences(a, b)
    assert diff is None, f"{what}: they differ in {diff}"


def content(img, background):
    box = ImageChops.difference(img, Image.new("RGB", img.size, background)).getbbox()
    assert box is not None, f"a {img.size} frame shows nothing but the background {background}"
    return box


def await_until(seconds, attempt):
    deadline = time.monotonic() + seconds
    while True:
        result, unmet = attempt()
        if unmet is None:
            return result
        if time.monotonic() > deadline:
            raise AssertionError(f"not within {seconds}s: {unmet}")
        time.sleep(0.25)


def await_screen(machine, name, settled, seconds):
    shots = []

    def attempt():
        shots.append(screenshot(machine, name))
        prev = shots[-2] if len(shots) > 1 else None
        return shots[-1], settled(prev, shots[-1])

    return await_until(seconds, attempt)


def steady_and(check):
    def settled(prev, shot):
        if prev is None:
            return "one screenshot is not enough to call it steady"
        diff = differences(prev, shot)
        if diff is not None:
            return f"two screenshots in a row differ in {diff}"
        return check(shot)

    return settled


def matching(expected, what):
    def settled(_, shot):
        diff = differences(expected, shot)
        return None if diff is None else f"the screen and {what} differ in {diff}"

    return settled
