import socket

noise = 3000
padding = "padding " * 8


class SlowShell(socket.socket):
    def recv(self, bufsize, flags=0):
        time.sleep(0.0005)
        return super().recv(bufsize, flags)


cut = '{"__MONOTONIC_TIMESTAMP":"1","MESSAGE":"cut off'
try:
    journal_entries(cut + "\n")
except ValueError as e:
    assert cut in str(e), f"the parse error does not name the line: {e}"
else:
    raise AssertionError("a line cut off in a string parsed")

start_all()
machine.wait_for_unit("multi-user.target")
machine.succeed(f"seq -f 'noise %.0f {padding}' {noise} | systemd-cat -t noise")
machine.wait_until_succeeds(f"journalctl -b -t noise -g '^noise {noise} '", timeout=seconds(60))
assert machine.shell is not None
machine.shell = SlowShell(fileno=machine.shell.detach())

try:
    whole_output(machine, "echo partial; false")
except Exception as e:
    assert "echo partial; false" in str(e), f"the failure does not name the command: {e}"
else:
    raise AssertionError("a failing command read as output")

messages = [msg for _, _, msg in journal(machine)]
want = [f"noise {i} {padding}".strip() for i in range(1, noise + 1)]
got = [msg.strip() for msg in messages if msg.startswith("noise ")]
assert got == want, f"the journal holds {len(got)} of the {noise} entries"

machine.succeed("echo 'noise after the first read' | systemd-cat -t noise")
machine.wait_until_succeeds("journalctl -b -t noise -g '^noise after the first read$'", timeout=seconds(60))
assert any(msg == "noise after the first read" for _, _, msg in journal(machine)), "a second read returned the first one's journal"
